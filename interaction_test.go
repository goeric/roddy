package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

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

// shortTimeout makes defaultTimeout the per-command budget for one test, so
// the deadline it asserts is the one the CLI would apply.
func shortTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := defaultTimeout
	defaultTimeout = d
	t.Cleanup(func() { defaultTimeout = previous })
}

// assertDeadline fails unless the browser or page carries one: the per-command
// budget is the browser's, and every page built from it inherits that context.
func assertDeadline(t *testing.T, what string, target interface{ GetContext() context.Context }) {
	t.Helper()
	if _, ok := target.GetContext().Deadline(); !ok {
		t.Errorf("%s carries no deadline", what)
	}
}

// hiddenActiveSession launches a browser of its own with two pages, points the
// saved session's active page at the hidden one, and returns its target id.
// retireFixture puts RODDY_HOME in a temp dir, so withPage reads this session.
func hiddenActiveSession(t *testing.T) proto.TargetTargetID {
	t.Helper()
	_, s := retireFixture(t)

	setup, err := connectBrowser(s)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	hidden, err := setup.Page(proto.TargetCreateTarget{URL: env.server.URL + "/"})
	if err != nil {
		t.Fatalf("open the first page: %v", err)
	}
	if _, err := setup.Page(proto.TargetCreateTarget{URL: env.server.URL + "/form"}); err != nil {
		t.Fatalf("open the second page: %v", err)
	}
	if got := visibilityState(t, hidden); got != "hidden" {
		t.Fatalf("first page visibilityState = %q, want %q -- this test proves nothing "+
			"against a foreground target", got, "hidden")
	}

	pages, err := setup.Pages()
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	s.ActivePage = -1
	for i, p := range pages {
		if p.TargetID == hidden.TargetID {
			s.ActivePage = i
		}
	}
	if s.ActivePage < 0 {
		t.Fatalf("target %s is not in the page list", hidden.TargetID)
	}
	mustSaveState(t, s)
	return hidden.TargetID
}

// stallListener accepts connections and never writes a byte, so a document
// request to it never commits and Page.navigate never answers.
func stallListener(t *testing.T) string {
	t.Helper()
	ln := listenLoopback(t)
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return "http://" + ln.Addr().String() + "/"
}

// The hazard raise() exists for: a backgrounded target runs no animation
// frames, so ScrollIntoView -> WaitStableRAF -> Page.WaitRepaint never
// resolves. WaitRepaint evals on p.root, which Page.Timeout does not re-point,
// so the one-second page deadline below does not end it either.
func TestScrollIntoView_HangsOnABackgroundedTarget(t *testing.T) {
	background, _ := openTwoPages(t)

	page := background.Timeout(time.Second)
	el, err := page.Element("h1")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}
	// Pin the block to WaitRepaint: WaitStableRAF waits for visibility first,
	// and that part does answer on a hidden target.
	if err := awaitError(t, 10*time.Second, "WaitVisible on a backgrounded target", el.WaitVisible); err != nil {
		t.Fatalf("WaitVisible on a backgrounded target: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- el.ScrollIntoView() }()

	select {
	case err := <-done:
		t.Fatalf("ScrollIntoView on a hidden target returned %v within 3s, past its 1s "+
			"page deadline; it is supposed to block on p.root's animation frame", err)
	case <-time.After(3 * time.Second):
	}
}

// The regression, with the raise the CLI now does before clicking. env.browser
// carries no deadline of its own, so without the raise this hangs outright
// rather than failing at a timeout.
func TestRaiseThenClick_BackgroundedTargetDoesNotHang(t *testing.T) {
	background, _ := openTwoPages(t)

	if _, err := background.Eval(
		`() => { document.getElementById('submit-btn').onclick = () => { window.clicked = true }; }`,
	); err != nil {
		t.Fatalf("arm the click handler: %v", err)
	}

	el, err := background.Timeout(30 * time.Second).Element("#submit-btn")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}
	if err := raise(background); err != nil {
		t.Fatalf("the raised page does not paint: %v", err)
	}

	if err := awaitError(t, 20*time.Second, "click on a raised target", func() error {
		return el.Click(proto.InputMouseButtonLeft, 1)
	}); err != nil {
		t.Fatalf("click on a raised target: %v", err)
	}

	res, err := background.Eval(`() => window.clicked === true`)
	if err != nil {
		t.Fatalf("read the click handler's flag: %v", err)
	}
	if !res.Value.Bool() {
		t.Error("the click did not reach the button")
	}
}

// paintFailure must fire on a tab that produces no frames and stay quiet on
// one that does -- whatever document.visibilityState claims, which is why the
// raised half reports that state instead of asserting on it.
func TestPaintFailure(t *testing.T) {
	background, _ := openTwoPages(t)

	err := paintFailure(background)
	if err == nil {
		t.Fatal("paintFailure on a target that is not rendered = nil, want the refusal")
	}
	if !strings.Contains(err.Error(), "no animation frame") ||
		!strings.Contains(err.Error(), "roddy page <i>") {
		t.Errorf("paintFailure = %q, want it to name the symptom and the way out", err)
	}

	bringToFront(background)
	if err := paintFailure(background); err != nil {
		t.Errorf("paintFailure on a raised target = %v, want nil (visibilityState = %q)",
			err, visibilityState(t, background))
	}
}

// raise() has to raise the page the session points at, not whatever the
// browser had in front.
func TestRaise_RaisesTheActivePage(t *testing.T) {
	hiddenID := hiddenActiveSession(t)

	_, browser, page := withPage()

	if page.TargetID != hiddenID {
		t.Fatalf("withPage returned target %s, want the session's active page %s",
			page.TargetID, hiddenID)
	}
	assertDeadline(t, "the browser withPage connected", browser)
	if got := visibilityState(t, page); got != "hidden" {
		t.Fatalf("the active page is %q before the raise, want %q", got, "hidden")
	}

	if err := raise(page); err != nil {
		t.Fatalf("raise the active page: %v", err)
	}

	// The frame is what the interaction waits for; visibilityState only
	// corroborates it on a browser with one activation in its history.
	if err := paintFailure(page); err != nil {
		t.Errorf("the raised active page does not paint: %v", err)
	}
	if got := visibilityState(t, page); got != "visible" {
		t.Errorf("visibilityState = %q after raise, want %q", got, "visible")
	}
}

// The reads go through withPage alone, which must leave the foreground where
// the user put it.
func TestWithPage_LeavesTheForegroundAlone(t *testing.T) {
	hiddenActiveSession(t)

	_, browser, page := withPage()

	assertDeadline(t, "the browser withPage connected", browser)
	if got := visibilityState(t, page); got != "hidden" {
		t.Errorf("visibilityState = %q after withPage, want %q -- a read raised the tab", got, "hidden")
	}
}

// The backstop for a target that cannot be raised: the deadline withPage puts
// on the BROWSER is the one WaitRepaint's p.root eval honours, so an
// interaction on a tab that never paints gives up on the budget instead of
// hanging.
func TestWithPage_BrowserDeadlineBoundsAnInteractionOnAHiddenTarget(t *testing.T) {
	shortTimeout(t, 2*time.Second)
	hiddenActiveSession(t)

	_, _, page := withPage()
	el, err := page.Element("h1")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}

	start := time.Now()
	err = awaitError(t, 20*time.Second, "ScrollIntoView under a browser deadline", el.ScrollIntoView)
	if err == nil {
		t.Fatal("ScrollIntoView on a hidden target = nil, want the deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ScrollIntoView = %v, want the context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 4*defaultTimeout {
		t.Errorf("ScrollIntoView took %v to give up, want it near the %v budget", elapsed, defaultTimeout)
	}
}

// Why wait <sel> and waitstable stay off raise(): neither waits on an
// animation frame, so neither needs the tab in front.
func TestWaitsThatDoNotNeedTheForeground(t *testing.T) {
	background, _ := openTwoPages(t)

	page := background.Timeout(20 * time.Second)
	el, err := page.Element("h1")
	if err != nil {
		t.Fatalf("element on the backgrounded page: %v", err)
	}
	if err := awaitError(t, 30*time.Second, "WaitVisible on a hidden target", el.WaitVisible); err != nil {
		t.Errorf("Element.WaitVisible on a hidden target: %v", err)
	}
	if err := awaitError(t, 30*time.Second, "WaitStable on a hidden target", func() error {
		return page.WaitStable(time.Second)
	}); err != nil {
		t.Errorf("Page.WaitStable on a hidden target: %v", err)
	}
}

// open and newpage report a Navigate that ran out of budget as a navigation
// that did not complete, not as a load that did not finish: Page.navigate
// answers only on commit, so a server that accepts the connection and sends
// nothing never lets it return.
func TestNavigateFailure_BoundsANeverAnsweringServer(t *testing.T) {
	shortTimeout(t, 3*time.Second)
	_, s := retireFixture(t)
	url := stallListener(t)

	browser, err := connectBrowserTimeout(s, defaultTimeout)
	if err != nil {
		t.Fatalf("connect with a deadline: %v", err)
	}

	start := time.Now()
	err = awaitError(t, 30*time.Second, "browser.Page against a stalled server", func() error {
		_, err := browser.Page(proto.TargetCreateTarget{URL: url})
		return err
	})
	if err == nil {
		t.Fatal("browser.Page against a server that never answers = nil, want the deadline")
	}
	if elapsed := time.Since(start); elapsed > 4*defaultTimeout {
		t.Errorf("browser.Page took %v to give up, want it near the %v budget", elapsed, defaultTimeout)
	}
	want := "navigation to " + url + " did not complete within " + defaultTimeout.String()
	if got := navigateFailure(err, url, ownSession); got != want {
		t.Errorf("navigateFailure = %q, want %q", got, want)
	}
}

// Anything that is not a deadline keeps navigationFailure's message and its
// hints.
func TestNavigateFailure_LeavesOtherErrorsToNavigationFailure(t *testing.T) {
	err := errors.New("net::ERR_CERT_AUTHORITY_INVALID")
	got := navigateFailure(err, "https://example.com/", ownSession)
	want := navigationFailure(err, ownSession)
	if got != want {
		t.Errorf("navigateFailure = %q, want %q", got, want)
	}
}

// One stuck tab stalls Pages() for every command, because rod calls Emulate on
// each page it builds. Say what that means and how to get out of it.
func TestListPagesFailure(t *testing.T) {
	shortTimeout(t, 7*time.Second)

	got := listPagesFailure(context.DeadlineExceeded).Error()
	for _, want := range []string{"did not answer within 7s", "stuck loading", "roddy stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("listPagesFailure = %q, want it to contain %q", got, want)
		}
	}

	other := errors.New("websocket closed")
	if got := listPagesFailure(other).Error(); got != "failed to list pages: websocket closed" {
		t.Errorf("listPagesFailure on a non-deadline error = %q, want the plain wrap", got)
	}
	if !errors.Is(listPagesFailure(other), other) {
		t.Error("listPagesFailure dropped the wrapped error")
	}
}

// open and newpage hand waitLoaded a page from browser.Page or Pages(), which
// carries the browser's context; the load wait has to be bounded by it. A page
// whose image never answers commits its document and never fires
// window.onload.
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

// rod hands a *rod.Page from Pages() straight back out of its cache, so a
// Timeout clone of a Browser that already built the page bounds nothing on it.
// Every roddy command connects fresh, which is what makes the browser deadline
// work at all.
func TestConnectBrowserTimeout_PagesInheritTheBrowserDeadline(t *testing.T) {
	_, s := retireFixture(t)

	browser, err := connectBrowserTimeout(s, 10*time.Second)
	if err != nil {
		t.Fatalf("connect with a deadline: %v", err)
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: env.server.URL + "/"})
	if err != nil {
		t.Fatalf("open a page: %v", err)
	}
	assertDeadline(t, "the page built by a deadlined browser", page)

	pages, err := browser.Pages()
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	for _, p := range pages {
		assertDeadline(t, "a page from Pages() on a deadlined browser", p)
	}
}
