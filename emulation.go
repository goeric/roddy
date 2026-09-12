package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func validateReducedMotion(value string) error {
	switch value {
	case "", "reduce", "no-preference":
		return nil
	default:
		return fmt.Errorf("reduced motion must be reduce, no-preference, or reset")
	}
}

func parseEmulateArgs(args []string) (string, error) {
	fs := flag.NewFlagSet("emulate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	value := fs.String("reduced-motion", "", "")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (len(args) > 0 && *value == "") {
		return "", fmt.Errorf("usage: roddy emulate [--reduced-motion reduce|no-preference|reset]")
	}
	if *value == "reset" {
		return "", nil
	}
	return *value, validateReducedMotion(*value)
}

func applyReducedMotion(page *rod.Page, value string) error {
	if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{
		{Name: "prefers-reduced-motion", Value: value},
	}}).Call(page); err != nil {
		return fmt.Errorf("failed to emulate reduced motion: %w", err)
	}
	return nil
}

func sessionPages(browser *rod.Browser, s *State) (rod.Pages, error) {
	pages, err := browser.Pages()
	if err != nil || s.ReducedMotion == "" {
		return pages, err
	}
	for _, page := range pages {
		if err := applyReducedMotion(page, s.ReducedMotion); err != nil {
			return nil, err
		}
	}
	return pages, nil
}

func newSessionPage(browser *rod.Browser, s *State, url string) (*rod.Page, error) {
	// Configure the blank target before navigation so startup matchMedia sees it.
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, err
	}
	if s.ReducedMotion != "" {
		err = applyReducedMotion(page, s.ReducedMotion)
	}
	if err == nil && url != "" {
		err = page.Navigate(url)
	}
	if err != nil {
		return nil, errors.Join(err, page.Close())
	}
	return page, nil
}

func cmdEmulate(args []string) {
	value, err := parseEmulateArgs(args)
	if err != nil {
		fatal("%v", err)
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowserTimeout(s, defaultTimeout)
	if err != nil {
		fatal("%v", err)
	}
	if len(args) > 0 {
		pages, err := browser.Pages()
		if err != nil {
			fatal("%v", listPagesFailure(err))
		}
		for _, page := range pages {
			if err := applyReducedMotion(page, value); err != nil {
				fatal("%v", err)
			}
		}
		s.ReducedMotion = value
		if err := saveState(s); err != nil {
			fatal("emulation changed, but failed to save it for later commands: %v", err)
		}
	}
	value = s.ReducedMotion
	if value == "" {
		value = "reset"
	}
	fmt.Printf("{\n  \"reduced_motion\": %q\n}\n", value)
}
