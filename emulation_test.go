package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func TestReducedMotionAcrossConnections(t *testing.T) {
	_, state := retireFixture(t)
	setup, err := connectBrowserTimeout(state, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	page := setup.MustPage(env.server.URL)
	defer page.MustClose()
	page.MustWaitLoad()
	if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-reduced-motion", Value: "no-preference"}}}).Call(page); err != nil {
		t.Fatal(err)
	}
	state.ReducedMotion = "reduce"
	mustSaveState(t, state)
	for i := 0; i < 2; i++ {
		s, err := loadState()
		if err != nil {
			t.Fatal(err)
		}
		browser, err := connectBrowserTimeout(s, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		active, err := getActivePage(browser, s)
		if err != nil {
			t.Fatal(err)
		}
		if !active.MustEval(`() => matchMedia('(prefers-reduced-motion: reduce)').matches`).Bool() {
			t.Fatalf("connection %d lost the reduced-motion preference", i)
		}
		active.MustReload().MustWaitLoad()
		if !active.MustEval(`() => matchMedia('(prefers-reduced-motion: reduce)').matches`).Bool() {
			t.Fatal("reload lost the preference")
		}
	}
}

func TestReducedMotionVariantsAndCSS(t *testing.T) {
	page := env.browser.MustPage(env.server.URL)
	defer page.MustClose()
	page.MustWaitLoad()
	baseline := page.MustEval(`() => matchMedia('(prefers-reduced-motion: reduce)').matches`).Bool()
	page.MustEval(`() => { document.head.insertAdjacentHTML('beforeend', '<style>body { color: red } @media (prefers-reduced-motion: reduce) { body { color: green } }</style>') }`)
	for _, mode := range []string{"reduce", "no-preference", ""} {
		if err := applyReducedMotion(page, mode); err != nil {
			t.Fatal(err)
		}
		want := mode == "reduce" || (mode == "" && baseline)
		if got := page.MustEval(`() => matchMedia('(prefers-reduced-motion: reduce)').matches`).Bool(); got != want {
			t.Fatalf("%q: matches=%v, want %v", mode, got, want)
		}
		wantColor := "rgb(255, 0, 0)"
		if want {
			wantColor = "rgb(0, 128, 0)"
		}
		if got := page.MustEval(`() => getComputedStyle(document.body).color`).Str(); got != wantColor {
			t.Fatalf("%q: color=%s, want %s", mode, got, wantColor)
		}
	}
}

func TestReducedMotionBeforeNewPageStartup(t *testing.T) {
	_, s := retireFixture(t)
	s.ReducedMotion = "reduce"
	browser, err := connectBrowserTimeout(s, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	page, err := newSessionPage(browser, s, `data:text/html,<script>window.initialMotion=matchMedia('(prefers-reduced-motion: reduce)').matches</script>`)
	if err != nil {
		t.Fatal(err)
	}
	defer page.MustClose()
	page.MustWaitLoad()
	if !page.MustEval(`() => window.initialMotion`).Bool() {
		t.Fatal("startup script did not see reduced motion")
	}
}

func TestParseEmulateArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""}, {[]string{"--reduced-motion", "reduce"}, "reduce"}, {[]string{"--reduced-motion=no-preference"}, "no-preference"}, {[]string{"--reduced-motion", "reset"}, ""},
	} {
		got, err := parseEmulateArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q = %q, %v", tc.args, got, err)
		}
	}
	for _, args := range [][]string{{"--reduced-motion"}, {"--reduced-motion="}, {"--reduced-motion", "false"}, {"--reduced-motion=reduce", "extra"}, {"--color-scheme=dark"}, {"--"}} {
		if _, err := parseEmulateArgs(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}

func TestReducedMotionCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<script>window.initialMotion=matchMedia('(prefers-reduced-motion: reduce)').matches</script>`))
	}))
	defer server.Close()
	_, s := retireFixture(t)
	s.Viewport = &viewportSize{Width: 320, Height: 568}
	mustSaveState(t, s)
	runCLI(t, 0, "emulate", "--reduced-motion", "reduce")
	runCLI(t, 0, "open", server.URL)
	runCLI(t, 0, "assert", "window.initialMotion === true")
	runCLI(t, 0, "assert", "matchMedia('(prefers-reduced-motion: reduce)').matches")
	runCLI(t, 0, "newpage", server.URL)
	runCLI(t, 0, "assert", "window.initialMotion === true")
	runCLI(t, 2, "emulate", "--reduced-motion", "invalid")
	runCLI(t, 0, "assert", "matchMedia('(prefers-reduced-motion: reduce)').matches")
	runCLI(t, 0, "emulate", "--reduced-motion", "no-preference")
	runCLI(t, 0, "reload")
	runCLI(t, 0, "assert", "window.initialMotion === false")
	runCLI(t, 0, "assert", "innerWidth", "320")
	runCLI(t, 0, "emulate", "--reduced-motion", "reset")
	state, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ReducedMotion != "" {
		t.Fatal("reset did not clear the saved preference")
	}
}

func TestNewSessionPageTimeoutClosesTarget(t *testing.T) {
	_, s := retireFixture(t)
	s.ReducedMotion = "reduce"
	browser, err := connectBrowserTimeout(s, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newSessionPage(browser, s, env.server.URL+"/stall")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("navigation error = %v", err)
	}
	observer, err := connectBrowserTimeout(s, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := (proto.TargetGetTargets{}).Call(observer)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets.TargetInfos {
		if target.Type == "page" {
			t.Errorf("failed navigation left target %s (%s)", target.TargetID, target.URL)
		}
	}
}
