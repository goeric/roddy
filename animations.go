package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/rod/lib/utils"
)

func extractWaitAnimations(args []string) ([]string, bool, error) {
	var remaining []string
	wait := false
	for i, arg := range args {
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		switch {
		case arg == "--wait-animations":
			wait = true
		case strings.HasPrefix(arg, "--wait-animations="):
			return nil, false, fmt.Errorf("--wait-animations does not take a value")
		default:
			remaining = append(remaining, arg)
		}
	}
	return remaining, wait, nil
}

func waitPageAnimations(page *rod.Page) error {
	budget := defaultTimeout
	if deadline, ok := page.GetContext().Deadline(); ok {
		budget = time.Until(deadline)
	}
	if budget <= 0 {
		return fmt.Errorf("waiting for animations: %w", context.DeadlineExceeded)
	}
	ctx, cancel := context.WithTimeout(page.GetContext(), budget)
	defer cancel()
	result, err := page.Context(ctx).Eval(waitAnimationsJS, budget.Milliseconds())
	if err != nil {
		return fmt.Errorf("waiting for animations: %w", err)
	}
	if !result.Value.Bool() {
		return fmt.Errorf("waiting for animations: %w", context.DeadlineExceeded)
	}
	return nil
}

// Polling also notices animations paused or canceled while the wait is active.
// The JS timer removes its rAF callback even if the CDP request times out first.
const waitAnimationsJS = `(timeout) => new Promise((resolve, reject) => {
 let frame, timer, quietFrames = 0;
 const finish = (error, complete = true) => {
  cancelAnimationFrame(frame);
  clearTimeout(timer);
  if (error) reject(error); else resolve(complete);
 };
 const tick = () => {
  try {
   const active = document.getAnimations().some(animation =>
    animation.playState === 'running' && animation.playbackRate !== 0 &&
    animation.timeline instanceof DocumentTimeline && animation.effect &&
    Number.isFinite(animation.effect.getComputedTiming().endTime));
   quietFrames = active ? 0 : quietFrames + 1;
   if (quietFrames >= 2) { finish(); return; }
   frame = requestAnimationFrame(tick);
  } catch (error) { finish(error); }
 };
 timer = setTimeout(() => finish(undefined, false), timeout);
 frame = requestAnimationFrame(tick);
})`

type elementScreenshotOptions struct {
	selector, file string
	waitAnimations bool
}

func parseElementScreenshotArgs(args []string) (elementScreenshotOptions, error) {
	args, wait, err := extractWaitAnimations(args)
	if err != nil {
		return elementScreenshotOptions{}, err
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) < 1 || len(args) > 2 {
		return elementScreenshotOptions{}, fmt.Errorf("usage: roddy screenshot-el [--wait-animations] <selector> [file]")
	}
	opts := elementScreenshotOptions{selector: args[0], file: "element.png", waitAnimations: wait}
	if len(args) == 2 {
		opts.file = args[1]
	}
	return opts, nil
}

func captureElementScreenshot(el *rod.Element, wait bool) ([]byte, error) {
	if !wait {
		return el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	}
	// Scroll without rod's geometric stability wait: an infinite transform must
	// not block capture when the animation wait explicitly ignores it.
	if err := (proto.DOMScrollIntoViewIfNeeded{ObjectID: el.Object.ObjectID}).Call(el); err != nil {
		return nil, err
	}
	page := el.Page().Context(el.GetContext())
	if err := waitPageAnimations(page); err != nil {
		return nil, err
	}
	data, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return nil, err
	}
	shape, err := el.Shape()
	if err != nil {
		return nil, err
	}
	box := shape.Box()
	return utils.CropImage(data, 0, int(box.X), int(box.Y), int(box.Width), int(box.Height))
}
