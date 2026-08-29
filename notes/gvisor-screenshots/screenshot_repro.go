//go:build ignore
// +build ignore

// Standalone test script to reproduce the gVisor screenshot issue.
// Tests 6 different Chrome configurations and reports which ones
// produce working screenshots.
//
// Usage: go run screenshot_repro.go
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func main() {
	chromeBin := launcher.NewBrowser().MustGet()
	fmt.Println("Chrome binary:", chromeBin)

	tests := []struct {
		name      string
		configure func(*launcher.Launcher) *launcher.Launcher
	}{
		{
			"standard headless",
			func(l *launcher.Launcher) *launcher.Launcher {
				return l.Set("no-sandbox").Set("disable-gpu").Set("disable-dev-shm-usage").Headless(true)
			},
		},
		{
			"--headless=new",
			func(l *launcher.Launcher) *launcher.Launcher {
				return l.Set("no-sandbox").Set("disable-gpu").Set("disable-dev-shm-usage").Delete("headless").Set("headless", "new")
			},
		},
		{
			"--enable-unsafe-swiftshader",
			func(l *launcher.Launcher) *launcher.Launcher {
				return l.Set("no-sandbox").Set("disable-gpu").Set("disable-dev-shm-usage").Set("enable-unsafe-swiftshader").Headless(true)
			},
		},
		{
			"--use-gl=angle --use-angle=swiftshader",
			func(l *launcher.Launcher) *launcher.Launcher {
				return l.Set("no-sandbox").Set("disable-dev-shm-usage").Set("use-gl", "angle").Set("use-angle", "swiftshader").Headless(true)
			},
		},
		{
			"--disable-gpu --in-process-gpu",
			func(l *launcher.Launcher) *launcher.Launcher {
				return l.Set("no-sandbox").Set("disable-gpu").Set("disable-dev-shm-usage").Set("in-process-gpu").Headless(true)
			},
		},
		{
			"--single-process --disable-gpu (THE FIX)",
			func(l *launcher.Launcher) *launcher.Launcher {
				return l.Set("no-sandbox").Set("disable-gpu").Set("disable-dev-shm-usage").Set("single-process").Headless(true)
			},
		},
	}

	for i, tt := range tests {
		fmt.Printf("\n=== Test %d: %s ===\n", i+1, tt.name)
		result, elapsed := tryScreenshot(chromeBin, tt.configure)
		if result != "" {
			fmt.Printf("FAILED after %v: %s\n", elapsed, result)
		} else {
			fmt.Printf("OK in %v\n", elapsed)
		}
	}
}

func tryScreenshot(bin string, configure func(*launcher.Launcher) *launcher.Launcher) (string, time.Duration) {
	l := launcher.New().Bin(bin).Leakless(true)
	l = configure(l)

	debugURL, err := l.Launch()
	if err != nil {
		return fmt.Sprintf("launch failed: %v", err), 0
	}
	pid := l.PID()
	defer func() {
		p, _ := os.FindProcess(pid)
		if p != nil {
			p.Kill()
		}
	}()

	browser := rod.New().ControlURL(debugURL).MustConnect()

	page := browser.MustPage("data:text/html,<h1>Hello</h1>").MustWaitLoad()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result, err := proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	}.Call(page.Context(ctx))
	elapsed := time.Since(start)

	if err != nil {
		return err.Error(), elapsed
	}
	fmt.Printf("  Screenshot: %d bytes\n", len(result.Data))
	return "", elapsed
}
