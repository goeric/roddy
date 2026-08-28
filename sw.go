package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
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

// waitServiceWorker polls until exactly one matching service worker is running.
// An MV3 worker starts asynchronously after launch, so a brief wait covers the
// startup race; it does not wake a worker Chrome has already suspended.
func waitServiceWorker(browser *rod.Browser, extID string, timeout time.Duration) (swTarget, error) {
	deadline := time.Now().Add(timeout)
	for {
		workers, err := listServiceWorkers(browser, extID)
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
		if time.Now().After(deadline) {
			suffix := ""
			if extID != "" {
				suffix = " for extension " + extID
			}
			return swTarget{}, fmt.Errorf("no extension service worker%s (it may not have started, or Chrome suspended it after idle)", suffix)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// evalInServiceWorker evaluates expr inside the worker, awaiting promises.
// Workers reject the Page abstraction ("Operation is only supported for
// pages"), so this attaches a flat session and speaks Runtime.evaluate.
func evalInServiceWorker(browser *rod.Browser, sw swTarget, expr string) (gson.JSON, error) {
	att, err := proto.TargetAttachToTarget{TargetID: sw.TargetID, Flatten: true}.Call(browser)
	if err != nil {
		return gson.JSON{}, fmt.Errorf("failed to attach to service worker: %w", err)
	}
	sess := browser.PageFromSession(att.SessionID)

	res, err := proto.RuntimeEvaluate{
		Expression:    expr,
		AwaitPromise:  true,
		ReturnByValue: true,
	}.Call(sess)
	if err != nil {
		return gson.JSON{}, err
	}
	if res.ExceptionDetails != nil {
		detail := res.ExceptionDetails.Text
		if ex := res.ExceptionDetails.Exception; ex != nil && ex.Description != "" {
			detail = ex.Description
		}
		return gson.JSON{}, fmt.Errorf("JS error: %s", detail)
	}
	return res.Result.Value, nil
}

// cmdSW handles "roddy sw" (list workers) and "roddy sw eval <expr>".
// The subcommand comes first, flags after: roddy sw eval --ext ID <expr>.
func cmdSW(args []string) {
	sub := "list"
	if len(args) > 0 && args[0] == "eval" {
		sub = "eval"
		args = args[1:]
	}

	fs := flag.NewFlagSet("sw", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ext := fs.String("ext", "", "")
	timeout := fs.Duration("timeout", 5*time.Second, "")
	if err := fs.Parse(args); err != nil {
		fatal("%v", err)
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	switch sub {
	case "list":
		if len(fs.Args()) > 0 {
			fatal("usage: roddy sw [--ext ID] [eval <expression>]")
		}
		workers, err := listServiceWorkers(browser, *ext)
		if err != nil {
			fatal("failed to list targets: %v", err)
		}
		for _, w := range workers {
			fmt.Printf("%s  %s\n", w.ExtensionID, w.URL)
		}
	case "eval":
		expr := strings.Join(fs.Args(), " ")
		if expr == "" {
			fatal("usage: roddy sw eval [--ext ID] [--timeout DUR] <expression>")
		}
		sw, err := waitServiceWorker(browser, *ext, *timeout)
		if err != nil {
			fatal("%v", err)
		}
		v, err := evalInServiceWorker(browser, sw, expr)
		if err != nil {
			fatal("%v", err)
		}
		printJSValue(v)
	}
}
