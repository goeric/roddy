package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// --- formatting ---

func TestFormatConsoleArg(t *testing.T) {
	str := gson.New("hello")
	num := gson.New(42)
	cases := []struct {
		name string
		arg  *proto.RuntimeRemoteObject
		want string
	}{
		{"string unquoted", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeString, Value: str}, "hello"},
		{"number", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeNumber, Value: num}, "42"},
		{"null", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeObject, Subtype: proto.RuntimeRemoteObjectSubtypeNull}, "null"},
		{"undefined", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeUndefined}, "undefined"},
		{"NaN", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeNumber, UnserializableValue: "NaN"}, "NaN"},
		{"object preview", &proto.RuntimeRemoteObject{
			Type: proto.RuntimeRemoteObjectTypeObject,
			Preview: &proto.RuntimeObjectPreview{
				Type: proto.RuntimeObjectPreviewTypeObject,
				Properties: []*proto.RuntimePropertyPreview{
					{Name: "x", Type: proto.RuntimePropertyPreviewTypeNumber, Value: "1"},
					{Name: "y", Type: proto.RuntimePropertyPreviewTypeString, Value: "a"},
				},
			},
		}, `{x: 1, y: "a"}`},
		{"truncated preview", &proto.RuntimeRemoteObject{
			Type: proto.RuntimeRemoteObjectTypeObject,
			Preview: &proto.RuntimeObjectPreview{
				Type:     proto.RuntimeObjectPreviewTypeObject,
				Overflow: true,
				Properties: []*proto.RuntimePropertyPreview{
					{Name: "x", Type: proto.RuntimePropertyPreviewTypeNumber, Value: "1"},
				},
			},
		}, "{x: 1, …}"},
		// An array preview drops the property names, which are just indices.
		{"array preview", &proto.RuntimeRemoteObject{
			Type:    proto.RuntimeRemoteObjectTypeObject,
			Subtype: proto.RuntimeRemoteObjectSubtypeArray,
			Preview: &proto.RuntimeObjectPreview{
				Type:    proto.RuntimeObjectPreviewTypeObject,
				Subtype: proto.RuntimeObjectPreviewSubtypeArray,
				Properties: []*proto.RuntimePropertyPreview{
					{Name: "0", Type: proto.RuntimePropertyPreviewTypeNumber, Value: "1"},
					{Name: "1", Type: proto.RuntimePropertyPreviewTypeNumber, Value: "2"},
				},
			},
		}, "[1, 2]"},
		{"truncated array preview", &proto.RuntimeRemoteObject{
			Type:    proto.RuntimeRemoteObjectTypeObject,
			Subtype: proto.RuntimeRemoteObjectSubtypeArray,
			Preview: &proto.RuntimeObjectPreview{
				Type:     proto.RuntimeObjectPreviewTypeObject,
				Subtype:  proto.RuntimeObjectPreviewSubtypeArray,
				Overflow: true,
				Properties: []*proto.RuntimePropertyPreview{
					{Name: "0", Type: proto.RuntimePropertyPreviewTypeNumber, Value: "1"},
				},
			},
		}, "[1, …]"},
		{"description fallback", &proto.RuntimeRemoteObject{
			Type:        proto.RuntimeRemoteObjectTypeFunction,
			Description: "function f() {}",
		}, "function f() {}"},
	}
	for _, c := range cases {
		if got := formatConsoleArg(c.arg); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFormatLogEntry(t *testing.T) {
	cases := []struct {
		name  string
		entry *proto.LogLogEntry
		want  string
	}{
		{"with url", &proto.LogLogEntry{
			Source: proto.LogLogEntrySourceNetwork,
			Level:  proto.LogLogEntryLevelError,
			Text:   "Failed to load resource",
			URL:    "http://example.com/missing",
		}, "[network error] Failed to load resource (http://example.com/missing)"},
		{"without url", &proto.LogLogEntry{
			Source: proto.LogLogEntrySourceDeprecation,
			Level:  proto.LogLogEntryLevelWarning,
			Text:   "old API",
		}, "[deprecation warning] old API"},
	}
	for _, c := range cases {
		if got := formatLogEntry(c.entry); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// --- parseLogsFlags ---

func TestParseLogsFlags(t *testing.T) {
	f, err := parseLogsFlags([]string{"--sw", "--follow", "--ext", "abc", "--timeout", "2s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.sw || !f.follow || f.ext != "abc" || f.timeout != 2*time.Second {
		t.Errorf("got %+v", f)
	}

	f, err = parseLogsFlags([]string{"-f"})
	if err != nil || !f.follow {
		t.Errorf("-f: got %+v, err %v", f, err)
	}

	// The inline "--flag=VAL" form takes the same values.
	f, err = parseLogsFlags([]string{"--sw", "--ext=abc", "--timeout=2s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.sw || f.ext != "abc" || f.timeout != 2*time.Second {
		t.Errorf("inline form: got %+v", f)
	}

	if f, err = parseLogsFlags(nil); err != nil || f.timeout != 5*time.Second {
		t.Errorf("default timeout: got %+v, err %v", f, err)
	}

	bad := [][]string{
		{"--ext", "abc"},   // --ext without --sw
		{"stray"},          // a positional argument
		{"--bogus"},        // an unknown flag
		{"--timeout"},      // a value flag with nothing after it
		{"--timeout=nope"}, // an unparseable duration
		{"--timeout=0s"},   // a timeout that can never collect anything
		{"--timeout=-5s"},  // a negative timeout
		{"--sw", "--ext="}, // an empty --ext is an unset shell variable
		// Bool flags take no value: "--follow=false" must not read as true.
		{"--follow=false"},
		{"--sw=false"},
		{"-f=true"},
	}
	for _, args := range bad {
		if _, err := parseLogsFlags(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

// --- snapshotLines (no browser) ---

// TestSnapshotLinesSorts covers the load-bearing sort: Chrome replays the Log
// domain's buffer after Runtime's, so entries arrive out of timestamp order.
func TestSnapshotLinesSorts(t *testing.T) {
	ch := make(chan logLine, 3)
	ch <- logLine{30, "runtime second"}
	ch <- logLine{10, "runtime first"}
	ch <- logLine{20, "log entry"}

	lines, end := snapshotLines(ch, 50*time.Millisecond, 5*time.Second)
	if end != snapshotSettled {
		t.Errorf("got end %v, want snapshotSettled", end)
	}
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.text)
	}
	want := "runtime first|log entry|runtime second"
	if got := strings.Join(texts, "|"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSnapshotLinesDeadline covers a page that never goes quiet: the settle
// timer resets on every line, so only the overall deadline ends the gather.
func TestSnapshotLinesDeadline(t *testing.T) {
	ch := make(chan logLine)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for i := 0; ; i++ {
			select {
			case ch <- logLine{proto.RuntimeTimestamp(i), "chatty"}:
			case <-done:
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	start := time.Now()
	lines, end := snapshotLines(ch, 300*time.Millisecond, 400*time.Millisecond)
	if end != snapshotDeadline {
		t.Errorf("got end %v, want snapshotDeadline", end)
	}
	if len(lines) == 0 {
		t.Error("expected the lines collected before the deadline")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("gather ran for %v; the deadline should have ended it", elapsed)
	}
}

func TestIsMethodNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unknown method by code", &cdp.Error{Code: -32601, Message: "'Log.enable' wasn't found"}, true},
		{"unknown method by text", &cdp.Error{Code: -32000, Message: "'Log.enable' wasn't found"}, true},
		{"session gone", &cdp.Error{Code: -32001, Message: "Session with given id not found"}, false},
		{"not a cdp error", context.DeadlineExceeded, false},
	}
	for _, c := range cases {
		if got := isMethodNotFound(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSnapshotLinesDeadlineDrainsBuffer: lines already delivered must not be
// shed just because the deadline fired mid-read.
func TestSnapshotLinesDeadlineDrainsBuffer(t *testing.T) {
	ch := make(chan logLine, 8)
	for i := 0; i < 8; i++ {
		ch <- logLine{proto.RuntimeTimestamp(i), "buffered"}
	}
	lines, end := snapshotLines(ch, time.Minute, time.Nanosecond)
	if end != snapshotDeadline {
		t.Errorf("got end %v, want snapshotDeadline", end)
	}
	if len(lines) != 8 {
		t.Errorf("got %d lines, want all 8 buffered before the deadline", len(lines))
	}
}

func TestSnapshotLinesClosedStream(t *testing.T) {
	ch := make(chan logLine, 1)
	ch <- logLine{10, "before the target went away"}
	close(ch)

	lines, end := snapshotLines(ch, time.Minute, time.Minute)
	if end != snapshotClosed {
		t.Errorf("got end %v, want snapshotClosed", end)
	}
	if len(lines) != 1 || lines[0].text != "before the target went away" {
		t.Errorf("collected lines lost on close: %v", lines)
	}
}

// --- log streams against a real browser ---

// collectDeadline bounds the browser tests' gathers; they assert on settling,
// not on the deadline, so it only has to be far larger than the settle window.
const collectDeadline = 20 * time.Second

// TestLogStream_ReplaysPageConsole is the test that matters: console output
// emitted BEFORE the stream opens must still be delivered, because every roddy
// invocation is a fresh CDP client and would otherwise always be too late.
func TestLogStream_ReplaysPageConsole(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/logs-page")
	defer page.MustClose()
	page.MustWaitLoad()
	// Let the fixture's delayed throw fire before attaching.
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, env.browser, page.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}

	lines := collectLogLines(stream, time.Second)
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"[log] fixture log 7",
		"[warning] fixture warning",
		"fixture boom", // the uncaught error, via Runtime.exceptionThrown
	} {
		if !strings.Contains(text, want) {
			t.Errorf("replayed output missing %q; got:\n%s", want, text)
		}
	}
	// Replay must preserve order.
	if li, wi := strings.Index(text, "fixture log"), strings.Index(text, "fixture warning"); li > wi {
		t.Errorf("log and warning out of order:\n%s", text)
	}
}

// TestLogStream_ReplaysNetworkErrors covers the Log domain, whose entries the
// Runtime domain never carries: without Log.enable — or with it called before
// the subscription — the fixture's failed fetch would go unreported.
func TestLogStream_ReplaysNetworkErrors(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/logs-page")
	defer page.MustClose()
	page.MustWaitLoad()
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, env.browser, page.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}

	text := strings.Join(collectLogLines(stream, time.Second), "\n")
	var found string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[network") {
			found = line
		}
	}
	if found == "" {
		t.Fatalf("no browser log entry for the failed fetch; got:\n%s", text)
	}
	if !strings.Contains(found, "/missing-resource") {
		t.Errorf("network entry missing the request URL: %q", found)
	}
}

func TestLogStream_DeliversLiveEvents(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/empty")
	defer page.MustClose()
	page.MustWaitLoad()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, env.browser, page.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}
	// Drain any replay so the assertion below is strictly about a live event.
	collectLogLines(stream, 300*time.Millisecond)

	page.MustEval(`() => console.log("live line", 9)`)

	lines := collectLogLines(stream, 2*time.Second)
	if !strings.Contains(strings.Join(lines, "\n"), "[log] live line 9") {
		t.Errorf("live console event not delivered; got: %v", lines)
	}
}

// TestLogStream_IgnoresOtherTargets guards the session filter: every callback
// sees the whole browser's events, so a second tab's output must not leak into
// this stream.
func TestLogStream_IgnoresOtherTargets(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/empty")
	defer page.MustClose()
	other := env.browser.MustPage(env.server.URL + "/empty")
	defer other.MustClose()
	page.MustWaitLoad()
	other.MustWaitLoad()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, env.browser, page.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}
	collectLogLines(stream, 300*time.Millisecond)

	other.MustEval(`() => console.log("other tab line")`)
	// The followed page logs too, so the window is known to be wide enough for
	// the other tab's line to have shown up if it were leaking.
	page.MustEval(`() => console.log("followed tab line")`)

	text := strings.Join(collectLogLines(stream, time.Second), "\n")
	if !strings.Contains(text, "followed tab line") {
		t.Fatalf("the followed tab's own line never arrived; got:\n%s", text)
	}
	if strings.Contains(text, "other tab line") {
		t.Errorf("another tab's console leaked into the stream:\n%s", text)
	}
}

// TestLogStream_CancelClosesStream: --follow ends by cancelling the context,
// and a stream that never closes would deadlock the reader.
func TestLogStream_CancelClosesStream(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/empty")
	defer page.MustClose()
	page.MustWaitLoad()

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := openLogStream(ctx, env.browser, page.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}
	cancel()
	waitStreamClosed(t, stream)
}

// TestLogStream_TargetCloseClosesStream: closing the followed tab used to hang
// the reader forever. The stream must end, and say why.
func TestLogStream_TargetCloseClosesStream(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/empty")
	page.MustWaitLoad()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, env.browser, page.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}
	page.MustClose()

	waitStreamClosed(t, stream)
	if stream.endReason() != "target closed" {
		t.Errorf("got end reason %q, want %q", stream.endReason(), "target closed")
	}
}

func TestLogStream_ReplaysServiceWorkerConsole(t *testing.T) {
	browser, _ := launchWithSWExtension(t)

	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, browser, sw.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}

	lines := collectLogLines(stream, time.Second)
	if !strings.Contains(strings.Join(lines, "\n"), "[log] SW booted") {
		t.Errorf("service worker console not replayed; got: %v", lines)
	}
}

// collectLogLines drains the stream through the same helper snapshot mode
// uses, so the tests see the ordering the command prints.
func collectLogLines(s *logStream, settle time.Duration) []string {
	collected, _ := snapshotLines(s.lines, settle, collectDeadline)
	lines := make([]string, len(collected))
	for i, l := range collected {
		lines[i] = l.text
	}
	return lines
}

// waitStreamClosed fails unless the stream's channel closes promptly.
func waitStreamClosed(t *testing.T, s *logStream) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-s.lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the stream never closed")
		}
	}
}
