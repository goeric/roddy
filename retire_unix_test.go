//go:build linux || darwin

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The PID-bearing profile lock and the signals below are POSIX shapes;
// profileLockPID and waitPIDGone are stubs on Windows.

// sleepChild starts a process that stands in for a PID a state file names. It
// is Waited on in the background (a signalled child would otherwise linger as
// a zombie, still "alive" to signal 0) and killed on cleanup.
type sleepChild struct {
	pid    int
	exited chan struct{}
	err    error // read only after exited closes
}

func startSleepChild(t *testing.T) *sleepChild {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	c := &sleepChild{pid: cmd.Process.Pid, exited: make(chan struct{})}
	go func() {
		c.err = cmd.Wait()
		close(c.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-c.exited
	})
	return c
}

// waitExit returns how the child ended, failing the test if it is still
// running after d.
func (c *sleepChild) waitExit(t *testing.T, d time.Duration) error {
	t.Helper()
	select {
	case <-c.exited:
		return c.err
	case <-time.After(d):
		t.Fatalf("PID %d still running %v after retireSession", c.pid, d)
		return nil
	}
}

func (c *sleepChild) alive() bool {
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

// The old proxy helper has to go before the new start truncates the log it
// still holds open.
func TestRetireSession_SignalsALiveProxyHelper(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	ln := listenLoopback(t)
	defer ln.Close()
	helper := startSleepChild(t)

	retireSession(&State{
		DebugURL:  "ws://127.0.0.1:9222/devtools/browser/x",
		ProxyPID:  helper.pid,
		ProxyPort: ln.Addr().(*net.TCPAddr).Port,
	}, io.Discard)

	if err := helper.waitExit(t, 3*time.Second); err == nil || !strings.Contains(err.Error(), "signal: terminated") {
		t.Errorf("helper exit = %v, want signal: terminated", err)
	}
}

// A recorded helper PID whose port no longer answers is not that helper any
// more — after a reboot it can be anything.
func TestRetireSession_LeavesAProxyHelperWhosePortIsDead(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	stranger := startSleepChild(t)

	retireSession(&State{
		DebugURL:  "ws://127.0.0.1:9222/devtools/browser/x",
		ProxyPID:  stranger.pid,
		ProxyPort: deadPort(t),
	}, io.Discard)

	// A SIGTERM already sent lands well inside this; the wait only rules out
	// racing the check against the signal.
	time.Sleep(200 * time.Millisecond)
	if !stranger.alive() {
		t.Errorf("PID %d was signalled although nothing answered on its port", stranger.pid)
	}
}

// deadPort returns a loopback port nothing listens on.
func deadPort(t *testing.T) int {
	t.Helper()
	ln := listenLoopback(t)
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

// A Chrome roddy launched that stopped answering its debug socket still owns
// the profile the new launch wants: with its own lock as evidence, stop it.
func TestRetireSession_StopsAnUnresponsiveOwnedChrome(t *testing.T) {
	l, s := retireFixture(t)
	s.ChromePID = l.PID()
	s.DebugURL = deadDebugURL(t)
	mustSaveState(t, s)
	if pid, ok := profileLockPID(s.DataDir); !ok || pid != l.PID() {
		t.Fatalf("profileLockPID(%q) = %d, %v, want %d, true", s.DataDir, pid, ok, l.PID())
	}

	var notes bytes.Buffer
	retireSession(s, &notes)

	want := fmt.Sprintf("note: stopping the previous Chrome (PID %d), which was not answering on its debug socket\n", l.PID())
	if notes.String() != want {
		t.Errorf("notes = %q, want %q", notes.String(), want)
	}
	if pidAlive(l.PID()) {
		t.Errorf("Chrome (PID %d) still running: retireSession returned before it exited", l.PID())
	}
	if stateFileExists() {
		t.Error("state file kept, want it removed")
	}
}

// Without the profile lock naming it, the recorded PID is only a number: say
// what is wrong and signal nothing.
func TestRetireSession_LeavesAnUnresponsiveChromeWithoutEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock string // SingletonLock target, "" for no lock at all
	}{
		{name: "no lock"},
		{name: "lock names another process", lock: "some-host-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RODDY_HOME", t.TempDir())
			dataDir := t.TempDir()
			if tc.lock != "" {
				if err := os.Symlink(tc.lock, filepath.Join(dataDir, "SingletonLock")); err != nil {
					t.Fatalf("write lock: %v", err)
				}
			}
			stranger := startSleepChild(t)
			s := &State{DebugURL: deadDebugURL(t), ChromePID: stranger.pid, DataDir: dataDir}
			mustSaveState(t, s)

			var notes bytes.Buffer
			retireSession(s, &notes)

			want := fmt.Sprintf("note: previous Chrome (PID %d) is not answering on its debug socket; run 'roddy stop' to kill it if it is still running\n", stranger.pid)
			if notes.String() != want {
				t.Errorf("notes = %q, want %q", notes.String(), want)
			}
			time.Sleep(200 * time.Millisecond)
			if !stranger.alive() {
				t.Errorf("PID %d was signalled without evidence that it is the browser", stranger.pid)
			}
			if !stateFileExists() {
				t.Error("state file removed, want the stale session kept for 'roddy stop'")
			}
		})
	}
}

func TestProfileLockPID(t *testing.T) {
	dir := t.TempDir()
	if pid, ok := profileLockPID(dir); ok {
		t.Errorf("profileLockPID(no lock) = %d, true, want 0, false", pid)
	}
	if pid, ok := profileLockPID(""); ok {
		t.Errorf("profileLockPID(\"\") = %d, true, want 0, false", pid)
	}
	for _, tc := range []struct {
		target string
		want   int
	}{
		// Chrome writes "<hostname>-<pid>", and a hostname has dashes of its own.
		{target: "MacBook-Pro-6.local-60213", want: 60213},
		{target: "host-1", want: 1},
		{target: "hostname", want: 0},
		{target: "host-", want: 0},
		{target: "host-0", want: 0},
		{target: "host-12x", want: 0},
	} {
		t.Run(tc.target, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Symlink(tc.target, filepath.Join(dir, "SingletonLock")); err != nil {
				t.Fatalf("symlink: %v", err)
			}
			pid, ok := profileLockPID(dir)
			if pid != tc.want || ok != (tc.want != 0) {
				t.Errorf("profileLockPID(-> %s) = %d, %v, want %d, %v", tc.target, pid, ok, tc.want, tc.want != 0)
			}
		})
	}
}
