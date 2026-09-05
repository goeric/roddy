//go:build windows

package main

import (
	"os/exec"
	"time"
)

func setSysProcAttr(cmd *exec.Cmd) {
	// SysProcAttr.Setsid is not available on Windows; no-op on this platform
}

// profileLockPID has no Windows counterpart: Chrome's profile singleton there
// is a mutex on the "Local State" file, not a symlink naming a PID. No
// evidence means start never signals a browser that stopped answering.
func profileLockPID(dataDir string) (int, bool) {
	return 0, false
}

// proxyPIDAlive has no signal 0 to ask with on Windows (pidAlive reads every
// PID there as gone), so the port answering is the only evidence status has:
// claiming the PID is alive keeps it to that one fact.
func proxyPIDAlive(pid int) bool {
	return true
}

// pidGone has nothing to ask with on Windows — pidAlive reads every PID there
// as gone — so it never answers, and a browser is never retired on that
// evidence alone.
func pidGone(pid int) (gone, known bool) {
	return false, false
}

// waitPIDGone is only reached behind profileLockPID's evidence, which this
// platform never has.
func waitPIDGone(pid int, d time.Duration) {}
