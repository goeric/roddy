package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

const (
	swListUsage = "usage: roddy sw [list] [--ext ID] [--timeout DUR]"
	swEvalUsage = "usage: roddy sw eval <expression> [--ext ID] [--timeout DUR]"
)

// swTarget is one extension service worker exposed by the browser.
type swTarget struct {
	ExtensionID string
	URL         string
	TargetID    proto.TargetTargetID
}

// listServiceWorkers returns the extension service workers currently running,
// optionally filtered to one extension ID.
func listServiceWorkers(browser *rod.Browser, extID string) ([]swTarget, error) {
	list, err := proto.TargetGetTargets{}.Call(browser)
	if err != nil {
		return nil, err
	}
	var workers []swTarget
	for _, t := range list.TargetInfos {
		if t.Type != proto.TargetTargetInfoTypeServiceWorker {
			continue
		}
		id := extensionIDFromURL(t.URL)
		if id == "" {
			continue // a web page's service worker, not an extension's
		}
		if extID != "" && id != extID {
			continue
		}
		workers = append(workers, swTarget{ExtensionID: id, URL: t.URL, TargetID: t.TargetID})
	}
	return workers, nil
}

// extensionIDFromURL extracts the extension ID from a chrome-extension:// URL.
func extensionIDFromURL(url string) string {
	rest, ok := strings.CutPrefix(url, "chrome-extension://")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// waitServiceWorkers polls until at least one matching worker is running and at
// least want of them are, so a session that loaded several extensions does not
// report whichever worker started first as the whole picture. Whatever exists
// at the deadline is returned rather than an error. A zero timeout still takes
// exactly one snapshot.
func waitServiceWorkers(browser *rod.Browser, extID string, want int, timeout time.Duration) ([]swTarget, error) {
	deadline := time.Now().Add(timeout)
	for {
		workers, err := listServiceWorkers(browser, extID)
		if err != nil {
			return nil, err
		}
		if len(workers) > 0 && len(workers) >= want {
			return workers, nil
		}
		if time.Now().After(deadline) {
			return workers, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitServiceWorker waits for exactly one matching service worker. More than
// one match is an immediate error — the caller disambiguates with --ext, it is
// never polled until one remains. An MV3 worker starts asynchronously after
// launch, so a brief wait covers the startup race; it does not wake a worker
// Chrome has already suspended.
func waitServiceWorker(browser *rod.Browser, extID string, timeout time.Duration) (swTarget, error) {
	workers, err := waitServiceWorkers(browser, extID, 1, timeout)
	if err != nil {
		return swTarget{}, err
	}
	if len(workers) == 1 {
		return workers[0], nil
	}
	if len(workers) > 1 {
		lines := make([]string, len(workers))
		for i, w := range workers {
			lines[i] = fmt.Sprintf("  %s  %s", w.ExtensionID, w.URL)
		}
		return swTarget{}, fmt.Errorf("multiple service workers; pick one with --ext:\n%s",
			strings.Join(lines, "\n"))
	}
	suffix := ""
	if extID != "" {
		suffix = " for extension " + extID
	}
	return swTarget{}, fmt.Errorf("no extension service worker%s (it may not have started, or Chrome suspended it after idle)", suffix)
}

// checkExtensionFilter validates --ext against the extensions this session
// loaded. mustChoose also rejects an omitted --ext when several are loaded:
// eval would otherwise land in whichever worker happened to be running. A
// session made by "roddy connect" records no extensions, so both checks stay
// quiet there and waitServiceWorker's multiple-workers error is the backstop.
func checkExtensionFilter(extensions []extensionInfo, extID string, mustChoose bool) error {
	if len(extensions) == 0 {
		return nil
	}
	if extID == "" {
		if mustChoose && len(extensions) > 1 {
			return fmt.Errorf("several extensions are loaded; pick one with --ext:\n%s", extensionLines(extensions))
		}
		return nil
	}
	for _, e := range extensions {
		if e.ID == extID {
			return nil
		}
	}
	return fmt.Errorf("extension %s is not loaded; this session has:\n%s", extID, extensionLines(extensions))
}

func extensionLines(extensions []extensionInfo) string {
	lines := make([]string, len(extensions))
	for i, e := range extensions {
		lines[i] = fmt.Sprintf("  %s  %s", e.ID, e.Name)
	}
	return strings.Join(lines, "\n")
}

// evalInServiceWorker evaluates expr inside the worker, awaiting promises.
// Workers reject the Page abstraction ("Operation is only supported for pages,
// not workers"), so this attaches a flat session and speaks Runtime.evaluate.
func evalInServiceWorker(browser *rod.Browser, sw swTarget, expr string) (gson.JSON, error) {
	att, err := proto.TargetAttachToTarget{TargetID: sw.TargetID, Flatten: true}.Call(browser)
	if err != nil {
		return gson.JSON{}, fmt.Errorf("failed to attach to service worker %s: %w", sw.URL, err)
	}
	// An attached debugger pins the worker's idle-suspension clock, so detaching
	// matters if this is ever called repeatedly within one process.
	defer proto.TargetDetachFromTarget{SessionID: att.SessionID}.Call(browser)

	// The deadline goes on the browser clone rather than the session page: rod
	// builds a PageFromSession by hand and leaves its helpersLock nil, so
	// Page.Timeout panics. The page inherits the browser's context either way.
	sess := browser.Timeout(defaultTimeout).PageFromSession(att.SessionID)

	// Runtime.evaluate takes an expression where page.Eval takes a function to
	// call, so wrap it in an IIFE to match "roddy js": a bare "{a: 1}" would
	// otherwise parse as a labelled block. AwaitPromise awaits what it returns.
	res, err := proto.RuntimeEvaluate{
		Expression:    fmt.Sprintf(`(() => { return (%s); })()`, expr),
		AwaitPromise:  true,
		ReturnByValue: true,
	}.Call(sess)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return gson.JSON{}, fmt.Errorf("evaluation timed out after %s (does the promise ever settle?); ROD_TIMEOUT raises the limit", defaultTimeout)
		}
		return gson.JSON{}, err
	}
	if res.ExceptionDetails != nil {
		detail := res.ExceptionDetails.Text
		if ex := res.ExceptionDetails.Exception; ex != nil {
			// A thrown or rejected primitive carries no Description, only a
			// Value; without it the message is a bare "Uncaught (in promise)".
			if ex.Description != "" {
				detail = ex.Description
			} else if !ex.Value.Nil() {
				detail = ex.Value.JSON("", "")
			}
		}
		return gson.JSON{}, fmt.Errorf("JS error: %s", detail)
	}
	// NaN, ±Infinity and -0 cannot be JSON-encoded, so Chrome spells them here
	// and leaves Value empty, which would otherwise print as null.
	if res.Result.UnserializableValue != "" {
		return gson.New(string(res.Result.UnserializableValue)), nil
	}
	return res.Result.Value, nil
}

// cmdSW handles "roddy sw [list]" and "roddy sw eval <expr>". Flags may appear
// anywhere, and every argument check runs before the browser is touched.
func cmdSW(args []string) {
	usage := swUsage(args)
	ext, timeout, rest, err := parseSWFlags(args)
	if err != nil {
		fatal("%v\n%s", err, usage)
	}

	sub := "list"
	if len(rest) > 0 && (rest[0] == "list" || rest[0] == "eval") {
		sub = rest[0]
		rest = rest[1:]
	}
	expr := strings.Join(rest, " ")
	if sub == "eval" {
		if expr == "" {
			fatal("%s", usage)
		}
	} else if len(rest) > 0 {
		if strings.HasPrefix(rest[0], "-") {
			fatal("unknown flag: %s\n%s", rest[0], usage)
		}
		fatal("unknown sw subcommand: %q\n%s", rest[0], usage)
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	if err := checkExtensionFilter(s.Extensions, ext, sub == "eval"); err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	if sub == "eval" {
		sw, err := waitServiceWorker(browser, ext, timeout)
		if err != nil {
			fatal("%v", err)
		}
		v, err := evalInServiceWorker(browser, sw, expr)
		if err != nil {
			fatal("%v", err)
		}
		printJSValue(v)
		return
	}

	// Every loaded extension should end up with a worker, so wait for the full
	// set rather than printing whichever one won the startup race.
	want := 1
	if ext == "" && len(s.Extensions) > 1 {
		want = len(s.Extensions)
	}
	workers, err := waitServiceWorkers(browser, ext, want, timeout)
	if err != nil {
		fatal("failed to list targets: %v", err)
	}
	if len(workers) == 0 {
		fmt.Fprintln(os.Stderr, "no extension service workers")
		os.Exit(1)
	}
	for _, w := range workers {
		fmt.Printf("%s  %s\n", w.ExtensionID, w.URL)
	}
	if len(workers) < want {
		fmt.Fprintf(os.Stderr, "(%d of %d extension workers running; the rest may be suspended or not started)\n",
			len(workers), want)
	}
}

// swUsage picks the usage line for the subcommand named in args.
func swUsage(args []string) string {
	for _, a := range args {
		if a == "eval" {
			return swEvalUsage
		}
	}
	return swListUsage
}

// parseSWFlags pulls --ext and --timeout out of args wherever they appear, the
// way cmdAXNode pre-extracts --json, so they may sit either side of both the
// subcommand and the expression. Anything else is left untouched, so an
// expression starting with "-" (say "-1 + 2") still reaches the worker intact.
func parseSWFlags(args []string) (ext string, timeout time.Duration, rest []string, err error) {
	timeout = 5 * time.Second
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		switch name {
		case "--ext", "-ext", "--timeout", "-timeout":
		default:
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			if i+1 == len(args) {
				return "", 0, nil, fmt.Errorf("flag needs an argument: %s", name)
			}
			i++
			value = args[i]
		}
		if strings.HasSuffix(name, "ext") {
			ext = value
			continue
		}
		if timeout, err = time.ParseDuration(value); err != nil {
			return "", 0, nil, fmt.Errorf("invalid value %q for flag %s: %w", value, name, err)
		}
	}
	return ext, timeout, rest, nil
}
