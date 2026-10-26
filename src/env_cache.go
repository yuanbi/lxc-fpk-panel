package main

import "time"

const envCacheTTL = 8 * time.Second

// Env returns the cached LXC environment probe result (avoids spawning
// lxc-info/lxc-ls on every poll).
func (a *App) Env() LXCEnv {
	a.envMu.Lock()
	defer a.envMu.Unlock()
	if !a.envAt.IsZero() && time.Since(a.envAt) < envCacheTTL {
		return normalizeEnv(a.envCache)
	}
	a.envCache = detectEnv()
	a.envAt = time.Now()
	return normalizeEnv(a.envCache)
}

// normalizeEnv 保证切片字段序列化为 [] 而不是 null。
// 前端对 e.templates / e.problems / e.hints 直接取 .length，
// 一旦是 null 就会抛异常导致整页渲染中断（页面停在上一个视图）。
func normalizeEnv(e LXCEnv) LXCEnv {
	if e.Templates == nil {
		e.Templates = []string{}
	}
	if e.Problems == nil {
		e.Problems = []string{}
	}
	if e.Hints == nil {
		e.Hints = []string{}
	}
	return e
}

func (a *App) invalidateEnv() {
	a.envMu.Lock()
	a.envAt = time.Time{}
	a.envMu.Unlock()
}
