package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

const logsUsage = "usage: roddy logs [--follow|-f] [--sw] [--ext ID] [--timeout DUR]"

// logsSettle is how long a snapshot waits for the stream to stay quiet before
// calling the replay complete.
const logsSettle = 600 * time.Millisecond

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
		// The bool flags carry no value, the way every other bool in the CLI
		// works: "--follow=false" is a mistake, not a way to turn one off.
		case "--follow", "-follow", "-f":
			if inline {
				return logsFlags{}, fmt.Errorf("flag %s does not take a value", name)
			}
			f.follow = true
			continue
		case "--sw", "-sw":
			if inline {
				return logsFlags{}, fmt.Errorf("flag %s does not take a value", name)
			}
			f.sw = true
			continue
		case "--ext", "-ext", "--timeout", "-timeout":
		default:
			return logsFlags{}, fmt.Errorf("unknown argument: %s", args[i])
		}
		var err error
		if value, i, err = takeFlagValue(args, i, name, value, inline); err != nil {
			return logsFlags{}, err
		}
		switch name {
		case "--ext", "-ext":
			// An unset shell variable would otherwise read as "no filter".
			if value == "" {
				return logsFlags{}, fmt.Errorf("flag %s needs a non-empty value", name)
			}
			f.ext = value
		default: // --timeout, -timeout
			if f.timeout, err = time.ParseDuration(value); err != nil {
				return logsFlags{}, fmt.Errorf("invalid value %q for flag %s: %w", value, name, err)
			}
			if f.timeout <= 0 {
				return logsFlags{}, fmt.Errorf("flag %s needs a positive duration", name)
			}
		}
	}
	if f.ext != "" && !f.sw {
		return logsFlags{}, fmt.Errorf("--ext only applies to --sw")
	}
	return f, nil
}

// logStream is an open subscription to one target's console output. Its lines
// channel is closed when the stream ends, whether because the caller cancelled
// the context, the target went away, or the browser connection did.
type logStream struct {
	lines        <-chan logLine
	targetClosed bool
}

// endReason says why the stream stopped. It is only meaningful once lines is
// closed: the flag is written before that close, which is what publishes it.
func (s *logStream) endReason() string {
	if s.targetClosed {
		return "target closed"
	}
	return "browser connection lost"
}

// openLogStream attaches its own flat session to the target and streams that
// target's console messages, uncaught exceptions, and browser log entries.
//
// The subscriptions are registered BEFORE the domains are enabled: Chrome
// replays its buffered events on enable, so a subscription registered after
// the enable can miss the whole replay.
//
// The session is a dedicated one rather than one rod already opened, because
// replay only fires on a disable→enable transition: enabling a domain some
// other code already enabled replays nothing. A private session also keeps
// rod's domain enable/restore bookkeeping — which acts on the sessions it owns
// — from disabling Runtime in the middle of the stream.
//
// The stream closes when ctx is cancelled, when the target is closed or
// detached, or when the browser connection ends.
func openLogStream(ctx context.Context, browser *rod.Browser, targetID proto.TargetTargetID) (*logStream, error) {
	// A wedged target answers no CDP call, so the setup gets a deadline. The
	// event wait below deliberately does not: it runs for the whole stream.
	setup := browser.Timeout(defaultTimeout)
	att, err := proto.TargetAttachToTarget{TargetID: targetID, Flatten: true}.Call(setup)
	if err != nil {
		return nil, logSetupErr("failed to attach to target", err)
	}
	// The attachment is never detached: it lasts as long as the process, and
	// while following a worker it usefully pins the worker awake. Contrast
	// evalInServiceWorker, which detaches so repeated calls do not.

	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan logLine, 256)
	s := &logStream{lines: ch}

	// EachEvent subscribes when called, not when its wait func runs — the call
	// must complete before the enables below, or the replay races the
	// subscription and can be lost. Only the blocking wait goes in a goroutine.
	// (EachEvent also fires error-swallowed Runtime/Log enables of its own, but
	// at the browser-level session, so our flat session's replay is untouched.)
	wait := browser.Context(ctx).EachEvent(
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
		// A closed tab or a stopped worker sends nothing more, so the stream has
		// to end itself rather than wait forever for the next line.
		func(e *proto.TargetDetachedFromTarget) {
			if e.SessionID == att.SessionID {
				s.targetClosed = true
				cancel()
			}
		},
		func(e *proto.TargetTargetDestroyed) {
			if e.TargetID == targetID {
				s.targetClosed = true
				cancel()
			}
		},
	)
	// rod invokes the callbacks synchronously inside wait and returns only once
	// the context or the connection has ended, so nothing can send on ch after
	// wait returns and closing there is safe.
	go func() {
		wait()
		cancel()
		close(ch)
	}()

	sess := setup.PageFromSession(att.SessionID)
	if err := (proto.RuntimeEnable{}).Call(sess); err != nil {
		cancel()
		return nil, logSetupErr("failed to enable console events", err)
	}
	// The Log domain covers network failures and browser-generated warnings.
	// Chrome supports it on pages and on workers; a browser reached through
	// ROD_CHROME_BIN that does not answers "method not found", and losing those
	// entries beats refusing to stream. Any other failure is real: the stream
	// would silently drop output it promises.
	if err := (proto.LogEnable{}).Call(sess); err != nil {
		if !isMethodNotFound(err) {
			cancel()
			return nil, logSetupErr("failed to enable browser log events", err)
		}
		fmt.Fprintln(os.Stderr, "(browser log entries unavailable on this browser; console output only)")
	}
	return s, nil
}

// logSetupErr keeps a stalled target apart from an outright failure: a page
// spinning in a busy loop answers nothing, and a bare "context deadline
// exceeded" says neither which call gave up nor that the limit is raisable.
func logSetupErr(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: timed out after %s (is the target busy?); ROD_TIMEOUT raises the limit", what, defaultTimeout)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// isMethodNotFound reports whether err is CDP's "method not found" — how a
// browser answers a domain or command it does not implement.
func isMethodNotFound(err error) bool {
	var e *cdp.Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == -32601 || strings.Contains(e.Message, "wasn't found")
}

// snapshotEnd says why snapshotLines stopped gathering.
type snapshotEnd int

const (
	snapshotSettled  snapshotEnd = iota // the stream went quiet: the replay is complete
	snapshotDeadline                    // lines were still arriving when the deadline hit
	snapshotClosed                      // the target or the browser went away
)

// snapshotLines gathers a stream until it stays quiet for settle, until the
// overall deadline elapses, or until it closes — whichever comes first. The
// deadline is what makes a chatty page terminate at all: a page logging every
// few hundred milliseconds resets the quiet window forever.
func snapshotLines(ch <-chan logLine, settle, deadline time.Duration) ([]logLine, snapshotEnd) {
	var lines []logLine
	end := snapshotSettled
	expire := time.After(deadline)
gather:
	for {
		select {
		case l, ok := <-ch:
			if !ok {
				end = snapshotClosed
				break gather
			}
			lines = append(lines, l)
		case <-time.After(settle):
			break gather
		case <-expire:
			end = snapshotDeadline
			// Take what is already buffered — shedding half a replay burst at
			// the deadline would misreport how much the page had logged.
			for {
				select {
				case l, ok := <-ch:
					if !ok {
						break gather
					}
					lines = append(lines, l)
					continue
				default:
				}
				break gather
			}
		}
	}
	// Runtime and Log replay as separate bursts rather than interleaved, so the
	// buffered output only reads in order once sorted by timestamp.
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].ts < lines[j].ts })
	return lines, end
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
// by reference rather than by value, so this falls through value → preview →
// description. Live events supply a one-level preview; a replayed event's args
// carry the reference and a description but no preview, which is what makes
// the description fallback the one the replay exercises.
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
// is a snapshot — Chrome's replay of buffered events plus a short settle
// window, bounded by --timeout — while --follow keeps streaming until the
// target, the browser, or the process goes away.
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
		// Listing pages talks to every target, so a wedged one would hang this
		// before the stream is even open.
		page, err := getActivePage(browser.Timeout(defaultTimeout), s)
		if err != nil {
			fatal("%v", logSetupErr("failed to find the active page", err))
		}
		targetID = page.TargetID
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := openLogStream(ctx, browser, targetID)
	if err != nil {
		fatal("%v", err)
	}

	if f.follow {
		// Replay first, then live events, until the process is interrupted.
		for line := range stream.lines {
			fmt.Println(line.text)
		}
		// Reaching here means the stream ended on its own, which is a failure:
		// a normal --follow only ends by signal.
		fatal("%s", stream.endReason())
	}

	lines, end := snapshotLines(stream.lines, logsSettle, f.timeout)
	for _, l := range lines {
		fmt.Println(l.text)
	}
	switch end {
	case snapshotDeadline:
		fmt.Fprintf(os.Stderr, "(collection stopped after %s with output still arriving; use --follow for the rest)\n", f.timeout)
	case snapshotClosed:
		fatal("%s", stream.endReason())
	}
}
