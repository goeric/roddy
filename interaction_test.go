package main

import (
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// visibilityState and openTwoPages live in capture_test.go -- capturing and
// driving a page fail on the same cause, a backgrounded target.

// awaitError returns f's error, failing the test instead of hanging if f does
// not return within limit. The goroutine is left behind deliberately -- the
// call it is stuck in returns when the test's page is closed.
func awaitError(t *testing.T, limit time.Duration, what string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("%s did not return within %v", what, limit)
		return nil
	}
}

// The hazard the foregrounding exists for. A backgrounded target never runs
// requestAnimationFrame, so rod's ScrollIntoView -> WaitStableRAF ->
// Page.WaitRepaint never resolves; WaitRepaint evaluates on p.root, which
// Page.Timeout does not reach, so the page deadline does not save it either.
// Every interaction goes through ScrollIntoView. If this ever stops holding,
// withForegroundPage has stopped being load-bearing.
func TestScrollIntoView_HangsOnABackgroundedTarget(t *testing.T) {
	background, _ := openTwoPages(t)

	el, err := background.Timeout(30 * time.Second).Element("h1")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- el.ScrollIntoView() }()

	select {
	case err := <-done:
		t.Fatalf("ScrollIntoView on a hidden target returned %v; it is supposed to "+
			"block on an animation frame that never comes", err)
	case <-time.After(3 * time.Second):
	}
}

// The regression. Without the bringToFront withForegroundPage does, "roddy
// click" on a backgrounded page hangs forever rather than for the deadline.
func TestClick_BackgroundedTargetDoesNotHang(t *testing.T) {
	background, _ := openTwoPages(t)

	if _, err := background.Eval(
		`() => { document.getElementById('submit-btn').onclick = () => { window.clicked = true }; }`,
	); err != nil {
		t.Fatalf("arm the click handler: %v", err)
	}

	bringToFront(background)

	el, err := background.Timeout(30 * time.Second).Element("#submit-btn")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}
	if err := awaitError(t, 20*time.Second, "click on a backgrounded target", func() error {
		return el.Click(proto.InputMouseButtonLeft, 1)
	}); err != nil {
		t.Fatalf("click on a backgrounded target: %v", err)
	}

	res, err := background.Eval(`() => window.clicked === true`)
	if err != nil {
		t.Fatalf("read the click handler's flag: %v", err)
	}
	if !res.Value.Bool() {
		t.Error("the click did not reach the button")
	}
}

// The second half of the fix: the deadline withPage puts on the browser, not
// just on the page, is the one WaitRepaint's p.root eval honours. Activation
// is bypassed here on purpose -- this is what saves a command whose target
// cannot be raised.
//
// It needs its own browser: rod caches Pages by target id on the *Browser, so
// a Timeout clone of the shared one would hand back the pages the shared one
// already built, with its own deadline-free context.
func TestConnectBrowserTimeout_BoundsAnInteractionOnABackgroundedTarget(t *testing.T) {
	_, s := retireFixture(t)

	setup, err := connectBrowser(s)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	background, err := setup.Page(proto.TargetCreateTarget{URL: env.server.URL + "/"})
	if err != nil {
		t.Fatalf("open the first page: %v", err)
	}
	if _, err := setup.Page(proto.TargetCreateTarget{URL: env.server.URL + "/form"}); err != nil {
		t.Fatalf("open the second page: %v", err)
	}
	if got := visibilityState(t, background); got != "hidden" {
		t.Fatalf("first page visibilityState = %q, want %q -- this test proves nothing "+
			"against a foreground target", got, "hidden")
	}

	// A fresh connect, as every roddy command makes: nothing is cached, so the
	// pages inherit the browser's deadline.
	const budget = 3 * time.Second
	browser, err := connectBrowserTimeout(s, budget)
	if err != nil {
		t.Fatalf("connect with a deadline: %v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	var page *rod.Page
	for _, p := range pages {
		if p.TargetID == background.TargetID {
			page = p
		}
	}
	if page == nil {
		t.Fatalf("target %s is gone from the fresh connection's page list", background.TargetID)
	}
	el, err := page.Timeout(defaultTimeout).Element("h1")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}

	start := time.Now()
	err = awaitError(t, 20*time.Second, "ScrollIntoView under a browser deadline", el.ScrollIntoView)
	if err == nil {
		t.Fatal("ScrollIntoView on a hidden target = nil, want the deadline")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("ScrollIntoView = %v, want it to name the context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 4*budget {
		t.Errorf("ScrollIntoView took %v to give up, want it near the %v budget", elapsed, budget)
	}
}

// open and newpage hand waitLoaded a page from browser.Page, whose context is
// the browser's -- connectBrowser leaves that one with no deadline, so the
// load wait needs its own. A page whose image never answers commits its
// document and never fires window.onload.
func TestLoadFailure_DeadlineBoundsAPageThatNeverFinishesLoading(t *testing.T) {
	page, err := env.browser.Page(proto.TargetCreateTarget{URL: env.server.URL + "/never-loads"})
	if err != nil {
		t.Fatalf("open the never-loading fixture: %v", err)
	}
	t.Cleanup(func() { _ = page.Close() })

	const budget = 2 * time.Second
	start := time.Now()
	failure := awaitError(t, 20*time.Second, "loadFailure under a page deadline", func() error {
		return loadFailure(page.Timeout(budget), ownSession)
	})
	if failure == nil {
		t.Fatal("loadFailure on a page whose load event never fires = nil, want the deadline")
	}
	const want = "page did not finish loading: context deadline exceeded"
	if failure.Error() != want {
		t.Errorf("loadFailure = %q, want %q", failure, want)
	}
	if elapsed := time.Since(start); elapsed > 4*budget {
		t.Errorf("loadFailure took %v to give up, want it near the %v budget", elapsed, budget)
	}
}
