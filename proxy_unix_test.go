//go:build linux || darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Signalling a recorded PID is a POSIX shape, and only here can a PID be made
// reliably gone: sleepChild (retire_unix_test.go) reaps the process it starts.

// stop kills the helper it recorded — and says so, so "Chrome stopped" is not
// the whole story.
func TestStopRecordedProxy_SignalsALiveHelper(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	port := livePort(t)
	helper := startSleepChild(t)

	var out bytes.Buffer
	stopRecordedProxy(&State{ProxyPID: helper.pid, ProxyPort: port}, &out)

	if err := helper.waitExit(t, 3*time.Second); err == nil || !strings.Contains(err.Error(), "signal: terminated") {
		t.Errorf("helper exit = %v, want signal: terminated", err)
	}
	if want := fmt.Sprintf("stopping proxy helper (PID %d)\n", helper.pid); out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A recorded PID whose port no longer answers is not that helper any more:
// SIGTERM would hit whatever the system handed that number to next.
func TestStopRecordedProxy_LeavesAHelperWhosePortIsDead(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	stranger := startSleepChild(t)

	var out bytes.Buffer
	stopRecordedProxy(&State{ProxyPID: stranger.pid, ProxyPort: deadPort(t)}, &out)

	// A SIGTERM already sent lands well inside this; the wait only rules out
	// racing the check against the signal.
	time.Sleep(200 * time.Millisecond)
	if !stranger.alive() {
		t.Errorf("PID %d was signalled although nothing answered on its port", stranger.pid)
	}
	if want := fmt.Sprintf("proxy helper already gone (PID %d)\n", stranger.pid); out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestStopRecordedProxy_SaysNothingForAnUnproxiedSession(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	var out bytes.Buffer
	stopRecordedProxy(&State{ChromePID: 4242}, &out)
	if out.Len() > 0 {
		t.Errorf("output = %q, want nothing for a session that never had a helper", out.String())
	}
}

// A port that answers for a PID that is gone belongs to another process: open
// must not send the user to a log nothing writes to any more.
func TestNavigationFailure_SquatterOnTheHelperPort(t *testing.T) {
	t.Setenv("RODDY_HOME", t.TempDir())
	port := livePort(t)
	gone := startSleepChild(t)
	signalPID(gone.pid)
	gone.waitExit(t, 3*time.Second)
	if pidAlive(gone.pid) {
		t.Fatalf("PID %d reads alive although it was reaped", gone.pid)
	}

	msg := navigationFailure(errors.New("net::ERR_PROXY_CONNECTION_FAILED"), &State{ChromePID: 4242, ProxyPID: gone.pid, ProxyPort: port})

	if want := fmt.Sprintf("port %d now belongs to another process", port); !strings.Contains(msg, want) {
		t.Errorf("navigationFailure = %q, want it to contain %q", msg, want)
	}
	if strings.Contains(msg, proxyLogPath()) {
		t.Errorf("navigationFailure = %q, want no pointer to the log of a helper that is gone", msg)
	}
}
