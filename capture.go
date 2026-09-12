package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type screenshotOptions struct {
	width, height int
	file          string
}

func parseScreenshotArgs(args []string) (screenshotOptions, error) {
	var opts screenshotOptions
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&opts.width, "width", 0, "")
	fs.IntVar(&opts.width, "w", 0, "")
	fs.IntVar(&opts.height, "height", 0, "")
	fs.IntVar(&opts.height, "h", 0, "")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	var sizeErr error
	fs.Visit(func(f *flag.Flag) {
		value := opts.width
		if f.Name == "height" || f.Name == "h" {
			value = opts.height
		}
		if value < 1 || value > maxViewportDimension {
			sizeErr = fmt.Errorf("screenshot dimensions must be integers from 1 to 10000000")
		}
	})
	if sizeErr != nil {
		return opts, sizeErr
	}
	if fs.NArg() > 1 {
		return opts, fmt.Errorf("usage: roddy screenshot [-w N] [-h N] [file]")
	}
	opts.file = fs.Arg(0)
	return opts, nil
}

func capturePageScreenshot(page *rod.Page, opts screenshotOptions) (data []byte, err error) {
	if opts.width != 0 || opts.height != 0 {
		var previous proto.EmulationSetDeviceMetricsOverride
		hadOverride := page.LoadState(&previous)
		view := previous
		if !hadOverride {
			metrics, err := proto.PageGetLayoutMetrics{}.Call(page)
			if err != nil {
				return nil, err
			}
			if metrics.CSSLayoutViewport == nil {
				return nil, fmt.Errorf("browser returned no layout viewport")
			}
			view.Width = metrics.CSSLayoutViewport.ClientWidth
			view.Height = metrics.CSSLayoutViewport.ClientHeight
		}
		if opts.width != 0 {
			view.Width = opts.width
		}
		if opts.height != 0 {
			view.Height = opts.height
		}
		defer func() {
			// Cleanup must still run when the capture exhausts its deadline.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var restore *proto.EmulationSetDeviceMetricsOverride
			if hadOverride {
				restore = &previous
			}
			if restoreErr := page.Context(ctx).SetViewport(restore); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("failed to restore viewport: %w", restoreErr))
			}
		}()
		if err := page.SetViewport(&view); err != nil {
			return nil, fmt.Errorf("failed to set screenshot viewport: %w", err)
		}
	}
	request := &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng}
	if opts.height == 0 {
		metrics, err := proto.PageGetLayoutMetrics{}.Call(page)
		if err != nil {
			return nil, err
		}
		if metrics.CSSContentSize == nil {
			return nil, fmt.Errorf("browser returned no content size")
		}
		// rod's full-page helper resizes the viewport, changing responsive layout.
		request.CaptureBeyondViewport = true
		request.Clip = &proto.PageViewport{X: metrics.CSSContentSize.X, Y: metrics.CSSContentSize.Y, Width: metrics.CSSContentSize.Width, Height: metrics.CSSContentSize.Height, Scale: 1}
	}
	return page.Screenshot(false, request)
}
