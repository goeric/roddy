package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

type lostMetricsReply struct {
	rod.CDPClient
	loseNext bool
}

func (c *lostMetricsReply) Call(ctx context.Context, session, method string, params interface{}) ([]byte, error) {
	result, err := c.CDPClient.Call(ctx, session, method, params)
	if err == nil && method == "Emulation.setDeviceMetricsOverride" && c.loseNext {
		c.loseNext = false
		return nil, context.DeadlineExceeded
	}
	return result, err
}

func TestScreenshotRestoresViewportAfterLostResizeReply(t *testing.T) {
	_, s := retireFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := cdp.StartWithURL(ctx, s.DebugURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	faulty := &lostMetricsReply{CDPClient: client}
	browser := rod.New().Context(ctx).Client(faulty).MustConnect()
	page := browser.MustPage(env.server.URL)
	page.MustWaitLoad()
	page.MustSetViewport(320, 568, 1, false)
	faulty.loseNext = true
	_, err = capturePageScreenshot(page, screenshotOptions{width: 375, height: 667})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture error = %v", err)
	}
	if got := page.MustEval(`() => JSON.stringify([innerWidth,innerHeight])`).Str(); got != "[320,568]" {
		t.Fatalf("viewport after a lost resize reply = %s", got)
	}
}
