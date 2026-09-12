package main

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

func animationPage(t *testing.T) *rod.Page {
	t.Helper()
	page := env.browser.MustPage(env.server.URL)
	t.Cleanup(func() { page.MustClose() })
	page.MustWaitLoad()
	page.MustEval(`() => { document.body.style.margin = '0'; document.body.innerHTML = '<div id="box" style="width:100px;height:100px;background:red"></div><div id="pulse"></div>'; }`)
	return page
}

func assertBlueCapture(t *testing.T, data []byte) {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	red, _, blue, _ := decoded.At(10, 10).RGBA()
	if red != 0 || blue != 65535 {
		t.Fatalf("capture is mid-animation: red=%d blue=%d", red, blue)
	}
}

func TestWaitAnimationsCapturesFinishedChainAndSkipsIdleMotion(t *testing.T) {
	page := animationPage(t)
	page.MustEval(`() => {
  window.finished = false;
  const box = document.querySelector('#box');
  const first = box.animate([{background:'red'}, {background:'green'}], {duration:150, fill:'forwards'});
  first.finished.then(() => box.animate([{background:'green'}, {background:'blue'}], {duration:150, fill:'forwards'}).finished).then(() => window.finished = true);
  window.pulse = document.querySelector('#pulse').animate([{opacity:0}, {opacity:1}], {duration:1000, iterations:Infinity});
  window.paused = document.body.animate([{opacity:1}, {opacity:1}], {duration:60000}); paused.pause();
  window.zeroRate = document.body.animate([{opacity:1}, {opacity:1}], {duration:60000}); zeroRate.playbackRate = 0;
 }`)
	data, err := capturePageScreenshot(page.Timeout(3*time.Second), screenshotOptions{waitAnimations: true})
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
	if !page.MustEval(`() => window.finished && pulse.playState === 'running' && paused.playState === 'paused' && zeroRate.playbackRate === 0`).Bool() {
		t.Fatal("animations were not allowed to finish naturally, or ignored animations were changed")
	}
}

func TestWaitAnimationsAfterViewportChange(t *testing.T) {
	page := animationPage(t)
	page.MustEval(`() => document.head.insertAdjacentHTML('beforeend', '<style>@keyframes paint {from {background:red} to {background:blue}} @media(max-width:400px) {#box {animation:paint 300ms forwards}}</style>')`)
	data, err := capturePageScreenshot(page.Timeout(3*time.Second), screenshotOptions{width: 320, height: 568, waitAnimations: true})
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
}

func TestWaitAnimationsTimeoutRestoresViewport(t *testing.T) {
	page := animationPage(t)
	page.MustSetViewport(320, 568, 1, false)
	page.MustEval(`() => document.querySelector('#box').animate([{opacity:0},{opacity:1}], {duration:60000})`)
	_, err := capturePageScreenshot(page.Timeout(150*time.Millisecond), screenshotOptions{width: 375, height: 667, waitAnimations: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %v", err)
	}
	if got := page.MustEval(`() => JSON.stringify([innerWidth,innerHeight])`).Str(); got != "[320,568]" {
		t.Fatalf("viewport after timeout = %s", got)
	}
}

func TestWaitAnimationsNoticesCancelAndPause(t *testing.T) {
	for _, action := range []string{"cancel", "pause"} {
		t.Run(action, func(t *testing.T) {
			page := animationPage(t)
			page.MustEval(`action => { const animation = document.querySelector('#box').animate([{opacity:0},{opacity:1}], {duration:60000}); setTimeout(() => animation[action](), 50); }`, action)
			if err := waitPageAnimations(page.Timeout(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScreenshotDoesNotWaitByDefault(t *testing.T) {
	page := animationPage(t)
	page.MustEval(`() => { window.entrance = document.querySelector('#box').animate([{opacity:0},{opacity:1}], {duration:60000}); }`)
	if _, err := capturePageScreenshot(page.Timeout(2*time.Second), screenshotOptions{}); err != nil {
		t.Fatal(err)
	}
	if !page.MustEval(`() => entrance.playState === 'running'`).Bool() {
		t.Fatal("default screenshot changed or finished the animation")
	}
}

func TestElementWaitAnimationsAfterScroll(t *testing.T) {
	page := animationPage(t)
	page.MustEval(`() => {
  const box = document.querySelector('#box'); box.style.marginTop = '2000px';
  window.observer = new IntersectionObserver(entries => { if (entries.some(entry => entry.isIntersecting)) { observer.disconnect(); box.animate([{background:'red'}, {background:'blue'}], {duration:300,fill:'forwards'}); } });
  observer.observe(box);
  box.animate([{transform:'translateX(0px)'},{transform:'translateX(5px)'}], {duration:1000,iterations:Infinity});
 }`)
	el := page.Timeout(3 * time.Second).MustElement("#box")
	data, err := captureElementScreenshot(el, true)
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
}

func TestScreenshotWaitAnimationsFlag(t *testing.T) {
	for _, args := range [][]string{{"--wait-animations", "-w", "320", "shot.png"}, {"shot.png", "--wait-animations"}} {
		opts, err := parseScreenshotArgs(args)
		if err != nil || !opts.waitAnimations {
			t.Fatalf("%q: %v", args, err)
		}
	}
	for _, args := range [][]string{{"--wait-animations", "#box", "shot.png"}, {"#box", "shot.png", "--wait-animations"}} {
		opts, err := parseElementScreenshotArgs(args)
		if err != nil || !opts.waitAnimations || opts.selector != "#box" || opts.file != "shot.png" {
			t.Fatalf("%q: %#v, %v", args, opts, err)
		}
	}
	for _, args := range [][]string{{"--wait-animations=false"}, {"--wait-animations=true"}} {
		if _, err := parseScreenshotArgs(args); err == nil {
			t.Errorf("screenshot accepted %q", args)
		}
		if _, err := parseElementScreenshotArgs(append(args, "#box")); err == nil {
			t.Errorf("screenshot-el accepted %q", args)
		}
	}
	opts, err := parseElementScreenshotArgs([]string{"#box"})
	if err != nil || opts.waitAnimations || opts.file != "element.png" {
		t.Fatalf("default options = %#v, %v", opts, err)
	}
	opts, err = parseElementScreenshotArgs([]string{"--", "--wait-animations"})
	if err != nil || opts.waitAnimations || opts.selector != "--wait-animations" {
		t.Fatalf("literal selector = %#v, %v", opts, err)
	}
}

func TestElementWaitAnimationsAfterLayoutMovement(t *testing.T) {
	page := animationPage(t)
	page.MustEval(`() => { const box=document.querySelector('#box'); box.style.background='blue'; box.animate([{marginTop:'0px'}, {marginTop:'1600px'}], {duration:400,fill:'forwards'}); }`)
	data, err := captureElementScreenshot(page.Timeout(3*time.Second).MustElement("#box"), true)
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 100 || config.Height != 100 {
		t.Fatalf("capture = %dx%d", config.Width, config.Height)
	}
}

func TestElementWaitAnimationsRetinaAndLargeElement(t *testing.T) {
	page := animationPage(t)
	page.MustSetViewport(320, 568, 2, false)
	page.MustEval(`() => { const box=document.querySelector('#box'); box.style.width='400px'; box.style.height='900px'; box.style.background='blue'; }`)
	data, err := captureElementScreenshot(page.Timeout(3*time.Second).MustElement("#box"), true)
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 800 || config.Height != 1800 {
		t.Fatalf("capture = %dx%d, want 800x1800", config.Width, config.Height)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	red, _, blue, _ := decoded.At(790, 1790).RGBA()
	if red != 0 || blue != 65535 {
		t.Fatal("pixels beyond the viewport were not captured")
	}
}

func TestAnimationScreenshotCLI(t *testing.T) {
	_, s := retireFixture(t)
	mustSaveState(t, s)
	runCLI(t, 0, "viewport", "320", "568")
	runCLI(t, 0, "open", env.server.URL)
	runCLI(t, 0, "js", `(() => { document.body.innerHTML='<div id="box" style="width:100px;height:100px;background:blue"></div>'; window.animation=document.querySelector('#box').animate([{background:'red'},{background:'blue'}],{duration:600,fill:'forwards'}); return true; })()`)
	file := filepath.Join(t.TempDir(), "page.png")
	runCLI(t, 0, "screenshot", "--wait-animations", file)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
	runCLI(t, 0, "js", `(() => { window.animation=document.querySelector('#box').animate([{background:'red'},{background:'blue'}],{duration:600,fill:'forwards'}); return true; })()`)
	file = filepath.Join(t.TempDir(), "element.png")
	runCLI(t, 0, "screenshot-el", "#box", file, "--wait-animations")
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	assertBlueCapture(t, data)
}
