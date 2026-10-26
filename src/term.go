package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const termRingSize = 128 * 1024

type TermSession struct {
	ID   string
	Name string
	pty  *os.File
	cmd  *exec.Cmd

	mu         sync.Mutex
	ring       bytes.Buffer
	subs       map[chan []byte]struct{}
	closed     bool
	lastActive time.Time
}

type TermManager struct {
	mu       sync.Mutex
	sessions map[string]*TermSession
}

func NewTermManager() *TermManager {
	m := &TermManager{sessions: map[string]*TermSession{}}
	go func() {
		t := time.NewTicker(time.Minute)
		for range t.C {
			m.reap()
		}
	}()
	return m
}

func (m *TermManager) reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.mu.Lock()
		idle := time.Since(s.lastActive)
		s.mu.Unlock()
		if idle > 30*time.Minute {
			s.Close()
			delete(m.sessions, id)
		}
	}
}

func (m *TermManager) Get(id string) *TermSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *TermManager) Create(name, shell string) (*TermSession, error) {
	if !validName(name) {
		return nil, errors.New("非法容器名")
	}
	info, err := lxcInfo(name)
	if err != nil {
		return nil, errors.New("容器状态读取失败")
	}
	if info.State != "RUNNING" {
		return nil, errors.New("容器未运行，请先启动容器")
	}
	if !haveCmd("lxc-attach") {
		return nil, errors.New("未找到 lxc-attach")
	}

	master, slave, err := openPTY()
	if err != nil {
		return nil, fmt.Errorf("创建虚拟终端失败: %v", err)
	}
	setWinsize(master, 120, 32)

	base := append(lxcPathArgs(), "-n", name, "--")
	cmd := exec.Command("lxc-attach", append(base, shell, "-l")...)
	if shell != "/bin/bash" {
		cmd = exec.Command("lxc-attach", append(base, shell, "-i")...)
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	cmd.SysProcAttr = ptySysProcAttr()

	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, fmt.Errorf("启动 shell 失败: %v", err)
	}
	slave.Close()

	s := &TermSession{
		ID:         newID("s_"),
		Name:       name,
		pty:        master,
		cmd:        cmd,
		subs:       map[chan []byte]struct{}{},
		lastActive: time.Now(),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.readLoop()
	return s, nil
}

func (s *TermSession) readLoop() {
	buf := make([]byte, 16*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.broadcast(chunk)
		}
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for ch := range s.subs {
			close(ch)
		}
		s.subs = map[chan []byte]struct{}{}
	}
	s.mu.Unlock()
	s.pty.Close()
}

func (s *TermSession) broadcast(b []byte) {
	s.mu.Lock()
	s.lastActive = time.Now()
	if s.ring.Len()+len(b) > termRingSize {
		keep := s.ring.Bytes()
		if len(keep) > termRingSize/2 {
			keep = keep[len(keep)-termRingSize/2:]
		}
		s.ring.Reset()
		s.ring.Write(keep)
	}
	s.ring.Write(b)
	for ch := range s.subs {
		select {
		case ch <- b:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *TermSession) Subscribe() ([]byte, chan []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backlog := make([]byte, s.ring.Len())
	copy(backlog, s.ring.Bytes())
	if s.closed {
		return backlog, nil, true
	}
	ch := make(chan []byte, 256)
	s.subs[ch] = struct{}{}
	return backlog, ch, false
}

func (s *TermSession) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *TermSession) Write(p []byte) error {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
	_, err := s.pty.Write(p)
	return err
}

func (s *TermSession) Resize(cols, rows int) {
	setWinsize(s.pty, cols, rows)
}

func (s *TermSession) Close() {
	s.mu.Lock()
	s.lastActive = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.pty.Close()
}

// ---------- http handlers ----------

type termFrame struct {
	D string `json:"d,omitempty"`
}

func (a *App) handleTerminalCreate(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	shell := "/bin/bash"
	if out, err := runCmd(15*time.Second, "lxc-attach", append(lxcPathArgs(), "-n", name, "--", "/bin/sh", "-c", "test -x /bin/bash && echo bash")...); err != nil || !strings.Contains(out, "bash") {
		shell = "/bin/sh"
	}
	s, err := a.terms.Create(name, shell)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": s.ID, "shell": shell})
}

func (a *App) handleTermStream(w http.ResponseWriter, r *http.Request, id string) {
	s := a.terms.Get(id)
	if s == nil {
		writeErr(w, 404, "终端会话不存在")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	backlog, ch, closed := s.Subscribe()
	if len(backlog) > 0 {
		b, _ := json.Marshal(termFrame{D: base64.StdEncoding.EncodeToString(backlog)})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	flusher.Flush()
	if closed {
		fmt.Fprint(w, "event: end\ndata: {}\n\n")
		flusher.Flush()
		return
	}
	defer s.Unsubscribe(ch)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case data, ok2 := <-ch:
			if !ok2 {
				fmt.Fprint(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			b, _ := json.Marshal(termFrame{D: base64.StdEncoding.EncodeToString(data)})
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (a *App) handleTermInput(w http.ResponseWriter, r *http.Request, id string) {
	s := a.terms.Get(id)
	if s == nil {
		writeErr(w, 404, "终端会话不存在")
		return
	}
	var body termFrame
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.D)
	if err != nil {
		writeErr(w, 400, "bad payload")
		return
	}
	if err := s.Write(raw); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.WriteHeader(204)
}

func (a *App) handleTermResize(w http.ResponseWriter, r *http.Request, id string) {
	s := a.terms.Get(id)
	if s == nil {
		writeErr(w, 404, "终端会话不存在")
		return
	}
	var body struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if body.Cols > 0 && body.Rows > 0 {
		s.Resize(body.Cols, body.Rows)
	}
	w.WriteHeader(204)
}

func (a *App) handleTermClose(w http.ResponseWriter, r *http.Request, id string) {
	if s := a.terms.Get(id); s != nil {
		s.Close()
		a.terms.mu.Lock()
		delete(a.terms.sessions, id)
		a.terms.mu.Unlock()
	}
	w.WriteHeader(204)
}
