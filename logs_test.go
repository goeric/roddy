package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// --- formatConsoleArg ---

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

	if _, err = parseLogsFlags([]string{"--ext", "abc"}); err == nil {
		t.Error("expected an error for --ext without --sw")
	}
	if _, err = parseLogsFlags([]string{"stray"}); err == nil {
		t.Error("expected an error for a stray argument")
	}
	if _, err = parseLogsFlags([]string{"--bogus"}); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}

// --- log streams against a real browser ---

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
	ch, err := openLogStream(ctx, env.browser, proto.TargetTargetID(page.TargetID))
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}

	lines := collectLogLines(ch, time.Second)
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

func TestLogStream_DeliversLiveEvents(t *testing.T) {
	page := env.browser.MustPage(env.server.URL + "/empty")
	defer page.MustClose()
	page.MustWaitLoad()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := openLogStream(ctx, env.browser, proto.TargetTargetID(page.TargetID))
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}
	// Drain any replay so the assertion below is strictly about a live event.
	collectLogLines(ch, 300*time.Millisecond)

	page.MustEval(`() => console.log("live line", 9)`)

	lines := collectLogLines(ch, 2*time.Second)
	if !strings.Contains(strings.Join(lines, "\n"), "[log] live line 9") {
		t.Errorf("live console event not delivered; got: %v", lines)
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
	ch, err := openLogStream(ctx, browser, sw.TargetID)
	if err != nil {
		t.Fatalf("openLogStream: %v", err)
	}

	lines := collectLogLines(ch, time.Second)
	if !strings.Contains(strings.Join(lines, "\n"), "[log] SW booted") {
		t.Errorf("service worker console not replayed; got: %v", lines)
	}
}

// collectLogLines drains the stream until it stays quiet for the settle
// duration, mirroring how the snapshot mode of cmdLogs gathers output.
func collectLogLines(ch <-chan logLine, settle time.Duration) []string {
	var lines []string
	for {
		select {
		case l := <-ch:
			lines = append(lines, l.text)
		case <-time.After(settle):
			return lines
		}
	}
}
