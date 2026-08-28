package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const logsUsage = "usage: roddy logs [--follow|-f] [--sw] [--ext ID] [--timeout DUR]"

// logLine is one console message, exception, or browser log entry.
type logLine struct {
	ts   proto.RuntimeTimestamp
	text string
}

type logsFlags struct {
	follow  bool
	sw      bool
	ext     string
	timeout time.Duration
}

// parseLogsFlags accepts the flags in any order; logs takes no positional
// arguments, so anything unrecognized is an error.
func parseLogsFlags(args []string) (logsFlags, error) {
	f := logsFlags{timeout: 5 * time.Second}
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		switch name {
		case "--follow", "-follow", "-f":
			f.follow = true
			continue
		case "--sw", "-sw":
			f.sw = true
			continue
		case "--ext", "-ext", "--timeout", "-timeout":
		default:
			return logsFlags{}, fmt.Errorf("unknown argument: %s", args[i])
		}
		if !inline {
			if i+1 == len(args) {
				return logsFlags{}, fmt.Errorf("flag needs an argument: %s", name)
			}
			i++
			value = args[i]
		}
		if strings.HasSuffix(name, "ext") {
			if value == "" {
				return logsFlags{}, fmt.Errorf("flag --ext needs a non-empty value")
			}
			f.ext = value
			continue
		}
		var err error
		if f.timeout, err = time.ParseDuration(value); err != nil {
			return logsFlags{}, fmt.Errorf("invalid value %q for flag %s: %w", value, name, err)
		}
	}
	if f.ext != "" && !f.sw {
		return logsFlags{}, fmt.Errorf("--ext only applies to --sw")
	}
	return f, nil
}

// openLogStream attaches its own flat session to the target and returns a
// channel of console messages, uncaught exceptions, and browser log entries.
// The subscriptions are registered BEFORE the domains are enabled: Chrome
// replays buffered events on enable, and delivers them only once per session —
// a session someone else enabled first (rod's own Pages() attach, say) has
// already spent its replay, which is why this never reuses an existing one.
// The stream closes when ctx is cancelled.
func openLogStream(ctx context.Context, browser *rod.Browser, targetID proto.TargetTargetID) (<-chan logLine, error) {
	att, err := proto.TargetAttachToTarget{TargetID: targetID, Flatten: true}.Call(browser)
	if err != nil {
		return nil, fmt.Errorf("failed to attach to target: %w", err)
	}

	b := browser.Context(ctx)
	ch := make(chan logLine, 256)
	// EachEvent subscribes when called, not when its wait func runs — the call
	// must complete before the enables below, or the replay races the
	// subscription and can be lost. Only the blocking wait goes in a goroutine.
	wait := b.EachEvent(
		func(e *proto.RuntimeConsoleAPICalled, sid proto.TargetSessionID) {
			if sid == att.SessionID {
				ch <- logLine{e.Timestamp, formatConsoleEvent(e)}
			}
		},
		func(e *proto.RuntimeExceptionThrown, sid proto.TargetSessionID) {
			if sid == att.SessionID {
				ch <- logLine{e.Timestamp, "[error] " + exceptionDetail(e.ExceptionDetails)}
			}
		},
		func(e *proto.LogEntryAdded, sid proto.TargetSessionID) {
			if sid == att.SessionID {
				ch <- logLine{e.Entry.Timestamp, formatLogEntry(e.Entry)}
			}
		},
	)
	go wait()

	sess := browser.PageFromSession(att.SessionID)
	if err := (proto.RuntimeEnable{}).Call(sess); err != nil {
		return nil, fmt.Errorf("failed to enable console events: %w", err)
	}
	// The Log domain covers network failures and browser-generated warnings.
	// Worker targets do not support it, so its absence is not an error.
	_ = (proto.LogEnable{}).Call(sess)
	return ch, nil
}

// formatConsoleEvent renders one console.* call: "[level] arg arg ...".
func formatConsoleEvent(e *proto.RuntimeConsoleAPICalled) string {
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = formatConsoleArg(a)
	}
	return fmt.Sprintf("[%s] %s", e.Type, strings.Join(args, " "))
}

// formatConsoleArg renders one console argument. Console events carry objects
// by reference with a one-level preview rather than a full value, so this
// falls through value → preview → description rather than assuming Value.
func formatConsoleArg(a *proto.RuntimeRemoteObject) string {
	switch {
	case a.UnserializableValue != "":
		return string(a.UnserializableValue)
	case a.Subtype == proto.RuntimeRemoteObjectSubtypeNull:
		return "null"
	case a.Type == proto.RuntimeRemoteObjectTypeUndefined:
		return "undefined"
	case !a.Value.Nil():
		if a.Type == proto.RuntimeRemoteObjectTypeString {
			return a.Value.Str()
		}
		return a.Value.JSON("", "")
	case a.Preview != nil:
		return formatPreview(a.Preview)
	case a.Description != "":
		return a.Description
	default:
		return string(a.Type)
	}
}

// formatPreview renders Chrome's one-level object preview: {x: 1, y: "a"}.
func formatPreview(p *proto.RuntimeObjectPreview) string {
	parts := make([]string, 0, len(p.Properties)+1)
	for _, prop := range p.Properties {
		v := prop.Value
		if prop.Type == proto.RuntimePropertyPreviewTypeString {
			v = `"` + v + `"`
		}
		if p.Subtype == proto.RuntimeObjectPreviewSubtypeArray {
			parts = append(parts, v)
		} else {
			parts = append(parts, prop.Name+": "+v)
		}
	}
	if p.Overflow {
		parts = append(parts, "…")
	}
	if p.Subtype == proto.RuntimeObjectPreviewSubtypeArray {
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// formatLogEntry renders a Log-domain entry (network failures, deprecations).
func formatLogEntry(e *proto.LogLogEntry) string {
	line := fmt.Sprintf("[%s %s] %s", e.Source, e.Level, e.Text)
	if e.URL != "" {
		line += " (" + e.URL + ")"
	}
	return line
}

// cmdLogs handles "roddy logs": print the target's console output. The default
// is a snapshot — Chrome's replay of buffered events plus a short settle window
// — while --follow keeps streaming until interrupted.
func cmdLogs(args []string) {
	f, err := parseLogsFlags(args)
	if err != nil {
		fatal("%v\n%s", err, logsUsage)
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	if f.sw {
		if err := checkExtensionFilter(s.Extensions, f.ext, true); err != nil {
			fatal("%v", err)
		}
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	var targetID proto.TargetTargetID
	if f.sw {
		sw, err := waitServiceWorker(browser, f.ext, f.timeout)
		if err != nil {
			fatal("%v", err)
		}
		targetID = sw.TargetID
	} else {
		page, err := getActivePage(browser, s)
		if err != nil {
			fatal("%v", err)
		}
		targetID = proto.TargetTargetID(page.TargetID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := openLogStream(ctx, browser, targetID)
	if err != nil {
		fatal("%v", err)
	}

	if f.follow {
		// Replay first, then live events, until the process is interrupted.
		for line := range ch {
			fmt.Println(line.text)
		}
		return
	}

	// Snapshot: gather the replay until the stream stays quiet, then order by
	// timestamp — Runtime and Log replay as separate bursts, not interleaved.
	var lines []logLine
	for {
		select {
		case l := <-ch:
			lines = append(lines, l)
			continue
		case <-time.After(600 * time.Millisecond):
		}
		break
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].ts < lines[j].ts })
	for _, l := range lines {
		fmt.Println(l.text)
	}
}
