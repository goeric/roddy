package main

import (
	"fmt"
	"os"
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
	data := fmt.Sprintf(`{"debug_url":%q,"reduced_motion":"reduce"}`, state.DebugURL)
	if err := os.WriteFile(statePath(), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
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
