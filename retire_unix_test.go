//go:build linux || darwin

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	return startChild(t, exec.Command("sleep", "30"))
}

// startStubbornChild stands in for a browser that will not take SIGTERM.
// SIGSTOP is not that browser: Darwin runs a stopped process's default SIGTERM
// action anyway (measured — it exited within 30ms). An ignored disposition
// does survive execve, so the sleep itself ignores the signal and only the
// cleanup's SIGKILL ends it. "ready" comes after the trap is installed: a
// SIGTERM arriving before it lands on the shell's default action instead
// (measured — the retire is only milliseconds behind the start).
func startStubbornChild(t *testing.T) *sleepChild {
	t.Helper()
	cmd := exec.Command("sh", "-c", `trap "" TERM; echo ready; exec sleep 30`)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	c := startChild(t, cmd)
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("stand-in did not announce itself: %q, %v", line, err)
	}
	return c
}

func startChild(t *testing.T, cmd *exec.Cmd) *sleepChild {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", cmd.Args, err)
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
		t.Fatalf("PID %d still running %v after it was signalled", c.pid, d)
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

// assertUnsignalled fails if c was signalled. A SIGTERM already sent lands well
// inside the wait; it only rules out racing the check against the signal.
func assertUnsignalled(t *testing.T, c *sleepChild) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if !c.alive() {
		t.Errorf("PID %d was signalled, want it left alone", c.pid)
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

	assertUnsignalled(t, stranger)
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
			if retireSession(s, &notes) {
				t.Error("retireSession = true, want false: nothing confirmed that browser gone")
			}

			want := fmt.Sprintf("note: previous Chrome (PID %d) is not answering on its debug socket; run 'roddy stop' to kill it if it is still running\n", stranger.pid)
			if notes.String() != want {
				t.Errorf("notes = %q, want %q", notes.String(), want)
			}
			assertUnsignalled(t, stranger)
			if !stateFileExists() {
				t.Error("state file removed, want the stale session kept for 'roddy stop'")
			}
		})
	}
}

// A Chrome that ignores SIGTERM outlives the state file that names it, so the
// record has to stay: nothing else could reach that browser again.
func TestRetireSession_ReportsAChromeThatIgnoresSIGTERM(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	dataDir := t.TempDir()
	stubborn := startStubbornChild(t)
	if err := os.Symlink(fmt.Sprintf("some-host-%d", stubborn.pid), filepath.Join(dataDir, "SingletonLock")); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	s := &State{DebugURL: deadDebugURL(t), ChromePID: stubborn.pid, DataDir: dataDir}
	mustSaveState(t, s)

	var notes bytes.Buffer
	if retireSession(s, &notes) {
		t.Error("retireSession = true, want false: the browser never exited")
	}

	if want := fmt.Sprintf("has not exited within %v of SIGTERM", retireExitTimeout); !strings.Contains(notes.String(), want) {
		t.Errorf("notes = %q, want it to contain %q", notes.String(), want)
	}
	if !stateFileExists() {
		t.Error("state file removed, want the session kept for 'roddy stop'")
	}
}

// A recorded PID that has left the process table is evidence enough on its
// own: the browser is gone, so the record it left behind may go too.
func TestRetireSession_RemovesTheRecordOfAChromeAlreadyGone(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	gone := startSleepChild(t)
	signalPID(gone.pid)
	gone.waitExit(t, 3*time.Second)
	if pidAlive(gone.pid) {
		t.Fatalf("PID %d reads alive although it was reaped", gone.pid)
	}
	// No lock at all: being gone is the only evidence this path has.
	s := &State{DebugURL: deadDebugURL(t), ChromePID: gone.pid, DataDir: t.TempDir()}
	mustSaveState(t, s)

	var notes bytes.Buffer
	if !retireSession(s, &notes) {
		t.Error("retireSession = false, want true: that browser is already gone")
	}

	want := fmt.Sprintf("note: previous Chrome (PID %d) is already gone\n", gone.pid)
	if notes.String() != want {
		t.Errorf("notes = %q, want %q", notes.String(), want)
	}
	if stateFileExists() {
		t.Error("state file kept, want the record of a browser that is gone removed")
	}
}

// connect writes a state file naming another browser, so a helper left behind
// would never be signalled by anything again: stop reads the new state and
// start sees ProxyPort 0.
func TestConnectState_RetiresAProxiedConnectSession(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	helper := startSleepChild(t)
	old := &State{
		DebugURL:  "ws://127.0.0.1:9222/devtools/browser/x",
		ProxyPID:  helper.pid,
		ProxyPort: livePort(t),
	}
	const debugURL = "ws://127.0.0.1:9333/devtools/browser/y"

	var notes bytes.Buffer
	save, err := connectState(old, debugURL, &notes)

	if err != nil {
		t.Fatalf("connectState = %v, want no error", err)
	}
	if err := helper.waitExit(t, 3*time.Second); err == nil || !strings.Contains(err.Error(), "signal: terminated") {
		t.Errorf("helper exit = %v, want signal: terminated", err)
	}
	want := fmt.Sprintf("note: replacing the connected session at %s; that browser stays running\n", debugHost(old.DebugURL))
	if notes.String() != want {
		t.Errorf("notes = %q, want %q", notes.String(), want)
	}
	if save.DebugURL != debugURL || save.ChromePID != 0 || save.ProxyPID != 0 || save.ProxyPort != 0 {
		t.Errorf("connectState = %+v, want a fresh connect session at %s", save, debugURL)
	}
}

// The issue's headline scenario in one call: a proxied start, then a connect
// somewhere else. Both the Chrome and the helper are roddy's to clear.
func TestConnectState_ClosesAProxiedOwnedChrome(t *testing.T) {
	l, old := retireFixture(t)
	old.ChromePID = l.PID()
	helper := startSleepChild(t)
	old.ProxyPID, old.ProxyPort = helper.pid, livePort(t)
	mustSaveState(t, old)
	const debugURL = "ws://127.0.0.1:9333/devtools/browser/elsewhere"

	var notes bytes.Buffer
	save, err := connectState(old, debugURL, &notes)

	if err != nil {
		t.Fatalf("connectState = %v, want no error", err)
	}
	if err := helper.waitExit(t, 3*time.Second); err == nil || !strings.Contains(err.Error(), "signal: terminated") {
		t.Errorf("helper exit = %v, want signal: terminated", err)
	}
	waitProcessGone(t, l.PID())
	if browser, err := connectBrowser(old); err == nil {
		_ = browser.Close()
		t.Error("owned browser still answering after connect retired it")
	}
	if save.DebugURL != debugURL || save.ChromePID != 0 || save.ProxyPID != 0 || save.ProxyPort != 0 {
		t.Errorf("connectState = %+v, want a fresh connect session at %s", save, debugURL)
	}
}

// Reconnecting to the browser roddy already drives at the address already
// recorded must retire nothing: the retire would close the very Chrome being
// attached to and drop the helper's record with it.
func TestConnectState_KeepsTheSessionForTheSameBrowser(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	chrome, helper := startSleepChild(t), startSleepChild(t)
	old := &State{
		DebugURL:  "ws://127.0.0.1:9222/devtools/browser/x",
		ChromePID: chrome.pid,
		ProxyPID:  helper.pid,
		ProxyPort: livePort(t),
	}
	mustSaveState(t, old)

	var notes bytes.Buffer
	save, err := connectState(old, old.DebugURL, &notes)

	if err != nil {
		t.Fatalf("connectState = %v, want no error", err)
	}
	if save != nil {
		t.Errorf("connectState = %+v, want nil: there is nothing to write", save)
	}
	if want := "note: already connected to this browser; session kept\n"; notes.String() != want {
		t.Errorf("notes = %q, want %q", notes.String(), want)
	}
	assertUnsignalled(t, chrome)
	assertUnsignalled(t, helper)
	if !stateFileExists() {
		t.Error("state file removed, want the session kept")
	}
}

// The same browser reached at a new address (a tunnel, or the localhost
// spelling) is a re-address, not a replacement: everything the session knows
// about that Chrome survives.
func TestConnectState_RecordsTheSameBrowserAtANewAddress(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	chrome, helper := startSleepChild(t), startSleepChild(t)
	old := &State{
		DebugURL:  "ws://127.0.0.1:9222/devtools/browser/x",
		ChromePID: chrome.pid,
		DataDir:   t.TempDir(),
		ProxyPID:  helper.pid,
		ProxyPort: livePort(t),
	}
	mustSaveState(t, old)
	const debugURL = "ws://localhost:9222/devtools/browser/x"

	var notes bytes.Buffer
	save, err := connectState(old, debugURL, &notes)

	if err != nil {
		t.Fatalf("connectState = %v, want no error", err)
	}
	want := *old
	want.DebugURL = debugURL
	if save == nil || !reflect.DeepEqual(*save, want) {
		t.Errorf("connectState = %+v, want %+v", save, want)
	}
	if got := fmt.Sprintf("note: already connected to this browser; recording it at %s\n", debugHost(debugURL)); notes.String() != got {
		t.Errorf("notes = %q, want %q", notes.String(), got)
	}
	assertUnsignalled(t, chrome)
	assertUnsignalled(t, helper)
	if !stateFileExists() {
		t.Error("state file removed, want the session kept for the caller to save")
	}
}

// A browser that could not be confirmed stopped must not lose the state file
// that names it: connect refuses instead, leaving 'roddy stop' a way back.
func TestConnectState_RefusesWhenTheOldChromeCannotBeConfirmedStopped(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	stranger := startSleepChild(t)
	// No lock in the data dir: nothing ties that PID to a browser.
	old := &State{DebugURL: deadDebugURL(t), ChromePID: stranger.pid, DataDir: t.TempDir()}
	mustSaveState(t, old)

	var notes bytes.Buffer
	save, err := connectState(old, "ws://127.0.0.1:9333/devtools/browser/y", &notes)

	if err == nil {
		t.Fatal("connectState returned no error, want a refusal")
	}
	if want := fmt.Sprintf("previous Chrome (PID %d) could not be confirmed stopped", stranger.pid); !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to contain %q", err, want)
	}
	if save != nil {
		t.Errorf("connectState = %+v, want nil: nothing may replace a session still in the way", save)
	}
	assertUnsignalled(t, stranger)
	if !stateFileExists() {
		t.Error("state file removed, want the session kept for 'roddy stop'")
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
