//go:build !linux

package main

import (
	"errors"
	"os"
	"syscall"
)

func openPTY() (*os.File, *os.File, error) {
	return nil, nil, errors.New("pseudo terminal is only supported on linux")
}

func setWinsize(f *os.File, cols, rows int) error { return nil }

func ptySysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
