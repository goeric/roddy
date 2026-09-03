//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// profileLockPID reads the PID out of Chrome's profile singleton lock.
// process_singleton_posix writes <dataDir>/SingletonLock as a symlink whose
// target is "<hostname>-<pid>" (the hostname may itself contain dashes, so the
// pid is the last field). A missing, foreign or unparseable lock reports no
// evidence — never a guess.
func profileLockPID(dataDir string) (int, bool) {
	if dataDir == "" {
		return 0, false
	}
	target, err := os.Readlink(filepath.Join(dataDir, "SingletonLock"))
	if err != nil {
		return 0, false
	}
	dash := strings.LastIndex(target, "-")
	if dash < 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(target[dash+1:])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// proxyPIDAlive reports whether the recorded proxy helper PID is still in the
// process table — the half of status's evidence that separates a helper from
// another process holding its port.
func proxyPIDAlive(pid int) bool {
	return pidAlive(pid)
}

// waitPIDGone blocks until pid leaves the process table, up to d; a pid that
// outlives d just returns.
func waitPIDGone(pid int, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}
