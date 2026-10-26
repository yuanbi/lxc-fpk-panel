package main

import (
	"sync"
	"time"
)

type Point struct {
	T   int64   `json:"t"`
	CPU float64 `json:"cpu"`
	Mem uint64  `json:"mem"`
}

const maxPoints = 360

type Sampler struct {
	mu          sync.RWMutex
	meta        *MetaStore
	interval    time.Duration
	hist        map[string][]Point
	lastCPU     map[string]cpuMark
	latest      []*Container
	lastScan    time.Time
	lastCollect time.Time
	lastErr     string

	hostHist  []Point
	hostTotal uint64
	hostIdle  uint64
	hostAt    time.Time
}

type cpuMark struct {
	seconds float64
	at      time.Time
}

func NewSampler(meta *MetaStore, interval time.Duration) *Sampler {
	if interval < 3*time.Second {
		interval = 3 * time.Second
	}
	return &Sampler{
		meta:     meta,
		interval: interval,
		hist:     map[string][]Point{},
		lastCPU:  map[string]cpuMark{},
	}
}

func (s *Sampler) Start() {
	go func() {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		s.tick()
		for range t.C {
			s.tick()
		}
	}()
}

func (s *Sampler) tick() { s.Snapshot() }

// Invalidate 丢弃 1.2 秒的采集缓存，切换容器存储目录后立即按新路径重扫。
func (s *Sampler) Invalidate() {
	s.mu.Lock()
	s.lastCollect = time.Time{}
	s.latest = nil
	s.mu.Unlock()
}

// Snapshot collects current state and appends a history point.
func (s *Sampler) Snapshot() []*Container {
	s.mu.Lock()
	if s.latest != nil && time.Since(s.lastCollect) < 1200*time.Millisecond {
		out := s.latest
		s.mu.Unlock()
		return out
	}
	s.mu.Unlock()

	list, err := collectContainers(s.meta)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCollect = now
	if err != nil {
		s.lastErr = err.Error()
		return s.latest
	}
	s.lastErr = ""
	for _, c := range list {
		prev, ok := s.lastCPU[c.Name]
		pct := 0.0
		if ok && c.State == "RUNNING" {
			dCPU := c.CPUSeconds - prev.seconds
			dT := now.Sub(prev.at).Seconds()
			if dCPU > 0 && dT > 0 {
				pct = dCPU / dT * 100
				if pct < 0 {
					pct = 0
				}
			}
		}
		c.CPUPercent = round2(pct)
		s.lastCPU[c.Name] = cpuMark{seconds: c.CPUSeconds, at: now}

		if now.Sub(s.lastScan) >= s.interval-transport() {
			if c.State == "RUNNING" || c.MemBytes > 0 {
				h := s.hist[c.Name]
				h = append(h, Point{T: now.Unix(), CPU: c.CPUPercent, Mem: c.MemBytes})
				if len(h) > maxPoints {
					h = h[len(h)-maxPoints:]
				}
				s.hist[c.Name] = h
			}
		}
	}
	// prune history of removed containers
	alive := map[string]bool{}
	for _, c := range list {
		alive[c.Name] = true
	}
	for name := range s.hist {
		if !alive[name] {
			delete(s.hist, name)
			delete(s.lastCPU, name)
		}
	}
	if now.Sub(s.lastScan) >= s.interval-transport() {
		s.lastScan = now
		// host level sample
		if cu, ok := readHostCPU(); ok {
			pct := 0.0
			if s.hostTotal > 0 && cu.total > s.hostTotal {
				dT := float64(cu.total - s.hostTotal)
				dI := float64(cu.idle - s.hostIdle)
				if dT > 0 && dI <= dT {
					pct = (dT - dI) / dT * 100
				}
			}
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			s.hostTotal, s.hostIdle = cu.total, cu.idle
			p := Point{T: now.Unix(), CPU: round2(pct), Mem: readMemUsed()}
			if s.hostTotal > 0 {
				s.hostHist = append(s.hostHist, p)
				if len(s.hostHist) > maxPoints {
					s.hostHist = s.hostHist[len(s.hostHist)-maxPoints:]
				}
			}
		}
	}
	s.latest = list
	return list
}

// HostHistory returns host level samples (cpu %, memory used bytes).
func (s *Sampler) HostHistory() []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Point, len(s.hostHist))
	copy(out, s.hostHist)
	return out
}

func transport() time.Duration { return 500 * time.Millisecond }

func (s *Sampler) History(name string) []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.hist[name]
	out := make([]Point, len(h))
	copy(out, h)
	return out
}

func (s *Sampler) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
