package main

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func TestViewportPersistsAcrossConnections(t *testing.T) {
	_, s := retireFixture(t)
	s.Viewport = &viewportSize{Width: 320, Height: 568}
	mustSaveState(t, s)
	var target proto.TargetTargetID
	for i := 0; i < 2; i++ {
		state, err := loadState()
		if err != nil {
			t.Fatal(err)
		}
		browser, err := connectBrowserTimeout(state, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			created, err := browser.Page(proto.TargetCreateTarget{URL: env.server.URL})
			if err != nil {
				t.Fatal(err)
			}
			target = created.TargetID
		}
		page, err := browser.PageFromTarget(target)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			defer page.Close()
		}
		page.MustWaitLoad()
		if got := page.MustEval(`() => JSON.stringify([innerWidth, innerHeight])`).String(); got != "[320,568]" {
			t.Fatalf("connection %d viewport = %s, want [320,568]", i, got)
		}
		page.MustReload().MustWaitLoad()
		if got := page.MustEval(`() => innerWidth`).Int(); got != 320 {
			t.Fatalf("width after reload = %d", got)
		}
	}
}

func TestScreenshotKeepsResponsiveLayout(t *testing.T) {
	page := env.browser.MustPage(env.server.URL)
	defer page.MustClose()
	page.MustWaitLoad()
	page.MustSetViewport(320, 568, 1, false)
	page.MustEval(`() => { document.body.style.margin = '0'; document.body.innerHTML = '<style>div { background: blue; } @media (min-width: 400px) { div { background: red; } }</style><div style="width:400px;height:1200px"></div>'; }`)
	data, err := capturePageScreenshot(page, screenshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	red, _, blue, _ := decoded.At(10, 10).RGBA()
	if red != 0 || blue != 65535 {
		t.Fatalf("full-page capture used desktop media query: red=%d blue=%d", red, blue)
	}
	if config.Width != 400 || config.Height != 1200 {
		t.Fatalf("image = %dx%d", config.Width, config.Height)
	}
	if got := page.MustEval(`() => JSON.stringify([innerWidth, innerHeight])`).String(); got != "[320,568]" {
		t.Fatalf("viewport after capture = %s", got)
	}
	data, err = capturePageScreenshot(page, screenshotOptions{width: 375, height: 667})
	if err != nil {
		t.Fatal(err)
	}
	config, err = png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 375 || config.Height != 667 {
		t.Fatalf("image = %dx%d", config.Width, config.Height)
	}
	if got := page.MustEval(`() => JSON.stringify([innerWidth, innerHeight])`).String(); got != "[320,568]" {
		t.Fatalf("viewport after temporary resize = %s", got)
	}
}

func TestViewportAndScreenshotArgs(t *testing.T) {
	for _, args := range [][]string{{"0", "568"}, {"-1", "568"}, {"320"}, {"320", "1.5"}, {"10000001", "568"}, {"reset", "extra"}} {
		if _, err := parseViewportArgs(args); err == nil {
			t.Errorf("viewport accepted %q", args)
		}
	}
	for _, args := range [][]string{{"-w", "0"}, {"-h", "-1"}, {"--height=10000001"}, {"one.png", "two.png"}} {
		if _, err := parseScreenshotArgs(args); err == nil {
			t.Errorf("screenshot accepted %q", args)
		}
	}
	for _, args := range [][]string{nil, {"-w", "320"}, {"--width=320", "--height=568", "mobile.png"}} {
		if _, err := parseScreenshotArgs(args); err != nil {
			t.Errorf("screenshot %q: %v", args, err)
		}
	}
	size, err := parseViewportArgs([]string{"reset"})
	if err != nil || size != nil {
		t.Fatalf("reset = %v, %v", size, err)
	}
	if got := viewportDevice(size).MetricsEmulation(); got.Width != 1280 || got.Height != 800 {
		t.Fatalf("reset dimensions = %dx%d", got.Width, got.Height)
	}
}
