package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Task struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Error    string   `json:"error"`
	Output   []string `json:"output"`
	Started  int64    `json:"started_at"`
	Finished int64    `json:"finished_at"`

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
	file *os.File
	done bool
}

func (t *Task) logf(line string) {
	t.mu.Lock()
	if len(t.Output) < 4000 {
		t.Output = append(t.Output, line)
	}
	if t.file != nil {
		t.file.WriteString(line + "\n")
	}
	for ch := range t.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	t.mu.Unlock()
}

func (t *Task) finish(err error) {
	t.mu.Lock()
	if err != nil {
		t.Status = "failed"
		t.Error = err.Error()
	} else {
		t.Status = "success"
	}
	t.Finished = time.Now().Unix()
	t.done = true
	for ch := range t.subs {
		close(ch)
	}
	t.subs = map[chan struct{}]struct{}{}
	if t.file != nil {
		t.file.Close()
	}
	t.mu.Unlock()
}

// Snapshot returns a copy safe for JSON encoding.
func (t *Task) Snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.Output))
	copy(out, t.Output)
	return map[string]any{
		"id": t.ID, "kind": t.Kind, "title": t.Title, "status": t.Status,
		"error": t.Error, "output": out, "started_at": t.Started,
		"finished_at": t.Finished,
	}
}

// Follow returns buffered output and a channel notified on new output.
func (t *Task) Follow() (out []string, ch chan struct{}, done bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out = make([]string, len(t.Output))
	copy(out, t.Output)
	if t.done {
		return out, nil, true
	}
	ch = make(chan struct{}, 8)
	t.subs[ch] = struct{}{}
	return out, ch, false
}

func (t *Task) Unfollow(ch chan struct{}) {
	if ch == nil {
		return
	}
	t.mu.Lock()
	if _, ok := t.subs[ch]; ok {
		delete(t.subs, ch)
		close(ch)
	}
	t.mu.Unlock()
}

type TaskManager struct {
	mu   sync.RWMutex
	dir  string
	all  map[string]*Task
	last map[string]*Task
}

func NewTaskManager(dir string) *TaskManager {
	os.MkdirAll(dir, 0o755)
	return &TaskManager{dir: dir, all: map[string]*Task{}, last: map[string]*Task{}}
}

func newID(prefix string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (m *TaskManager) Submit(kind, title string, fn func(logf func(string)) error) *Task {
	t := &Task{
		ID:      newID("t_"),
		Kind:    kind,
		Title:   title,
		Status:  "running",
		Output:  []string{},
		Started: time.Now().Unix(),
		subs:    map[chan struct{}]struct{}{},
	}
	if f, err := os.Create(filepath.Join(m.dir, t.ID+".log")); err == nil {
		t.file = f
	}
	m.mu.Lock()
	m.all[t.ID] = t
	m.last[kind] = t
	if len(m.all) > 60 {
		for id, old := range m.all {
			if id != t.ID && old.Status != "running" {
				delete(m.all, id)
				os.Remove(filepath.Join(m.dir, id+".log"))
				break
			}
		}
	}
	m.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.logf(fmt.Sprintf("内部错误: %v", r))
				t.finish(fmt.Errorf("%v", r))
			}
		}()
		err := fn(t.logf)
		t.finish(err)
	}()
	return t
}

func (m *TaskManager) Get(id string) *Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.all[id]
}

func (m *TaskManager) Last(kind string) *Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last[kind]
}

// runStream runs a command and streams its combined output to logf.
func runStream(logf func(string), name string, args ...string) error {
	cmd := exec.Command(name, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	logf("$ " + strings.Join(append([]string{name}, args...), " "))
	if err := cmd.Start(); err != nil {
		return err
	}
	buf := make([]byte, 32*1024)
	var pending []byte
	for {
		n, err := pipe.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				i := strings.IndexByte(string(pending), '\n')
				if i < 0 {
					break
				}
				logf(string(pending[:i]))
				pending = pending[i+1:]
			}
			if len(pending) > 0 && len(pending) < 32*1024 {
				continue
			}
		}
		if err != nil {
			break
		}
	}
	if len(pending) > 0 {
		logf(string(pending))
	}
	return cmd.Wait()
}

func encodeJSON(w any) ([]byte, error) { return json.Marshal(w) }
