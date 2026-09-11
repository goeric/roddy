package main

import (
	"fmt"
	"strconv"

	"github.com/go-rod/rod/lib/devices"
)

type viewportSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (v viewportSize) validate() error {
	if v.Width < 1 || v.Height < 1 || v.Width > 10000000 || v.Height > 10000000 {
		return fmt.Errorf("viewport width and height must be integers from 1 to 10000000")
	}
	return nil
}

func viewportDevice(size *viewportSize) devices.Device {
	device := devices.LaptopWithMDPIScreen.Landscape()
	if size != nil {
		device.Screen.Horizontal = devices.ScreenSize{Width: size.Width, Height: size.Height}
	}
	return device
}

func parseViewportArgs(args []string) (*viewportSize, error) {
	if len(args) == 1 && args[0] == "reset" {
		return nil, nil
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("usage: roddy viewport [<width> <height> | reset]")
	}
	width, werr := strconv.Atoi(args[0])
	height, herr := strconv.Atoi(args[1])
	size := &viewportSize{Width: width, Height: height}
	if werr != nil || herr != nil {
		return nil, fmt.Errorf("viewport width and height must be integers from 1 to 10000000")
	}
	if err := size.validate(); err != nil {
		return nil, err
	}
	return size, nil
}

func cmdViewport(args []string) {
	var size *viewportSize
	if len(args) > 0 {
		var err error
		size, err = parseViewportArgs(args)
		if err != nil {
			fatal("%v", err)
		}
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	if len(args) > 0 {
		s.Viewport = size
	}
	browser, err := connectBrowserTimeout(s, defaultTimeout)
	if err != nil {
		fatal("%v", err)
	}
	// Pages applies the session device to every existing tab; an empty session
	// can still be configured before the first open.
	if _, err := sessionPages(browser, s); err != nil {
		fatal("failed to apply viewport: %v", listPagesFailure(err))
	}
	if len(args) > 0 {
		if err := saveState(s); err != nil {
			fatal("viewport changed, but failed to save it for later commands: %v", err)
		}
	}
	metrics := viewportDevice(s.Viewport).MetricsEmulation()
	fmt.Printf("{\n  \"width\": %d,\n  \"height\": %d\n}\n", metrics.Width, metrics.Height)
}
