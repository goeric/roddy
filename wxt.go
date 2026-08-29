package main

import (
	"os"
	"path/filepath"
)

// wxtChromeOutput is where WXT builds the unpacked Chrome MV3 extension.
const wxtChromeOutput = ".output/chrome-mv3"

// wxtConfigNames are the config filenames WXT's loader accepts.
var wxtConfigNames = []string{
	"wxt.config.ts", "wxt.config.js", "wxt.config.mts",
	"wxt.config.mjs", "wxt.config.cts", "wxt.config.cjs",
}

type wxtDetection int

const (
	wxtNone    wxtDetection = iota
	wxtUnbuilt              // a wxt.config.* with no loadable build output
	wxtBuilt                // build output with a manifest, ready for Chrome
)

// detectWXT reports whether dir is a WXT project and whether it has a build
// Chrome could load. The manifest check means a half-deleted output counts as
// unbuilt, so the auto path never hands Chrome a broken directory.
func detectWXT(dir string) wxtDetection {
	config := false
	for _, name := range wxtConfigNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			config = true
			break
		}
	}
	if !config {
		return wxtNone
	}
	if _, err := os.Stat(filepath.Join(dir, wxtChromeOutput, "manifest.json")); err == nil {
		return wxtBuilt
	}
	return wxtUnbuilt
}

// wxtAuto decides what start does about a WXT project: the extension path to
// add, a notice for stdout, or a hint for stderr — all empty when the user
// already decided with --extension or --no-extension.
func wxtAuto(opts startOptions, det wxtDetection) (path, notice, hint string) {
	if opts.noExtension || len(opts.extensions) > 0 {
		return "", "", ""
	}
	switch det {
	case wxtBuilt:
		return wxtChromeOutput,
			"WXT project detected: loading .output/chrome-mv3 (pass --no-extension to skip)", ""
	case wxtUnbuilt:
		return "", "", `WXT project detected but .output/chrome-mv3 is missing; run "wxt build" and start again to load it`
	}
	return "", "", ""
}

// wxtTipWanted suggests --local only when auto-scoping is about to put this
// WXT project's browser state in the global session — never second-guessing
// an explicit --local/--global or a RODDY_HOME override.
func wxtTipWanted(mode scopeMode, roddyHome, stateDir, localDir string) bool {
	return mode == scopeAuto && roddyHome == "" && stateDir != localDir
}

const wxtLocalTip = "tip: use --local to keep this project's browser state isolated (./.roddy/)"
