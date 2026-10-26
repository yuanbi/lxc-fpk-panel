package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

/* ------------------------------------------------------------------
   管理密码
   密码以 PBKDF2-HMAC-SHA256 哈希保存在 ${TRIM_PKGVAR}/admin.json，
   安装向导里填写的密码由 cmd/install_callback 调用本程序 -set-pass 写入。
   会话 Cookie 使用 HMAC(会话密钥) 签名，无需存储会话表。
------------------------------------------------------------------ */

const (
	pbkdf2Iter    = 120000
	sessionCookie = "lxcpanel_sess"
	sessionMaxAge = 30 * 24 * 3600
	setupGrace    = 2 * time.Hour // 未设置密码时允许 UI 自助设置的时长
)

type adminFile struct {
	Password   string `json:"password,omitempty"`
	SessionKey string `json:"session_key"`
	UpdatedAt  int64  `json:"updated_at"`
}

// AdminStore 读取 admin.json，按 mtime 缓存，保证 CLI 改密能立刻对运行中的服务生效。
type AdminStore struct {
	mu     sync.Mutex
	path   string
	mtime  time.Time
	loaded bool
	cur    adminFile
}

func NewAdminStore(path string) *AdminStore {
	return &AdminStore{path: path}
}

func (a *App) adminFilePath() string {
	if a.admin == nil {
		return ""
	}
	return a.admin.path
}

func (s *AdminStore) Load() adminFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := os.Stat(s.path)
	if err == nil && s.loaded && st.ModTime().Equal(s.mtime) {
		return s.cur
	}
	var f adminFile
	if err == nil {
		if raw, e := os.ReadFile(s.path); e == nil {
			_ = json.Unmarshal(raw, &f)
		}
		s.mtime = st.ModTime()
	}
	if f.SessionKey == "" {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e == nil {
			f.SessionKey = hex.EncodeToString(b)
		} else {
			f.SessionKey = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		if e := s.saveLocked(f); e != nil {
			fmt.Fprintf(os.Stderr, "write admin.json: %v\n", e)
		}
	}
	s.cur, s.loaded = f, true
	return f
}

func (s *AdminStore) saveLocked(f adminFile) error {
	f.UpdatedAt = time.Now().Unix()
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.mtime = st.ModTime()
	}
	s.cur, s.loaded = f, true
	return nil
}

// SetPassword 保存新密码；password 为空表示清除密码。
func (s *AdminStore) SetPassword(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadNoLock()
	if password == "" {
		f.Password = ""
	} else {
		h, err := hashPassword(password)
		if err != nil {
			return err
		}
		f.Password = h
	}
	return s.saveLocked(f)
}

func (s *AdminStore) loadNoLock() adminFile {
	if s.loaded {
		return s.cur
	}
	return adminFile{}
}

/* ---------------- 哈希 ---------------- */

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	var idx [4]byte
	for b := 1; b <= blocks; b++ {
		binary.BigEndian.PutUint32(idx[:], uint32(b))
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(idx[:])
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(pw), salt, pbkdf2Iter, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

func verifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter <= 0 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(pw), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

/* ---------------- 会话 ---------------- */

func sessionValue(key string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte("lxcpanel-session-v1"))
	return "s1" + hex.EncodeToString(m.Sum(nil))
}

func (a *App) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	f := a.admin.Load()
	if f.SessionKey == "" {
		return false
	}
	want := sessionValue(f.SessionKey)
	if len(c.Value) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(want)) == 1
}

func (a *App) setSessionCookie(w http.ResponseWriter) {
	f := a.admin.Load()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sessionValue(f.SessionKey),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionMaxAge,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

/* ---------------- 登录限流 ---------------- */

type limEntry struct {
	fails int
	until time.Time
}

type loginLimiter struct {
	mu sync.Mutex
	m  map[string]*limEntry
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{m: map[string]*limEntry{}} }

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok {
		return false
	}
	if time.Now().After(e.until) {
		delete(l.m, ip)
		return false
	}
	return e.fails >= 6
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok || time.Now().After(e.until) {
		e = &limEntry{}
		l.m[ip] = e
	}
	e.fails++
	e.until = time.Now().Add(5 * time.Minute)
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}
