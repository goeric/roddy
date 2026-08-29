package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// wxtChromeOutput is where WXT builds the unpacked Chrome MV3 extension.
const wxtChromeOutput = ".output/chrome-mv3"

// wxtConfigNames are the code-file config forms WXT projects use in practice.
// Not loader parity: WXT's loader (c12) also accepts json/yaml/toml/rc forms.
var wxtConfigNames = []string{
	"wxt.config.ts", "wxt.config.js", "wxt.config.mts",
	"wxt.config.mjs", "wxt.config.cts", "wxt.config.cjs",
}

type wxtDetection int

const (
	wxtNone    wxtDetection = iota
	wxtUnbuilt              // a wxt.config.* with no build output directory
	wxtBuilt                // a wxt.config.* with one: a candidate, not a promise
)

// detectWXT reports whether dir is a WXT project and whether it has build
// output at all. Detection is sugar, so a config file that cannot be stat'd
// counts as absent; only fs.ErrNotExist on the output directory means unbuilt,
// which start turns into a "run wxt build" hint. Anything else is a candidate
// whose contents wxtStart resolves — a present-but-broken build degrades there.
func detectWXT(dir string) wxtDetection {
	hasConfig := false
	for _, name := range wxtConfigNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			hasConfig = true
			break
		}
	}
	if !hasConfig {
		return wxtNone
	}
	if _, err := os.Stat(filepath.Join(dir, wxtChromeOutput)); errors.Is(err, fs.ErrNotExist) {
		return wxtUnbuilt
	}
	return wxtBuilt
}

// wxtStart decides what start does about a WXT project in dir: the options to
// launch with, a notice for stdout, or a hint for stderr — the options
// unchanged and no output when the user already decided with --extension or
// --no-extension.
//
// A detected build is resolved here rather than trusted, so a broken one is a
// hint instead of a fatal error out of loadExtensions later. The path appended
// is the one resolved here: resolveExtension is deterministic, so loadExtensions
// resolving it a second time cannot reach a different directory.
func wxtStart(opts startOptions, dir, unpackRoot string) (out startOptions, notice, hint string) {
	if opts.noExtension || len(opts.extensions) > 0 {
		return opts, "", ""
	}
	switch detectWXT(dir) {
	case wxtBuilt:
		path := filepath.Join(dir, wxtChromeOutput)
		if _, err := resolveExtension(path, unpackRoot); err != nil {
			return opts, "", fmt.Sprintf(
				"WXT project detected but %s could not be loaded: %v (pass --no-extension to silence this)",
				wxtChromeOutput, err)
		}
		opts.extensions = append(opts.extensions, path)
		return opts, fmt.Sprintf(
			"WXT project detected: loading %s (pass --no-extension to skip)", wxtChromeOutput), ""
	case wxtUnbuilt:
		return opts, "", fmt.Sprintf(
			`WXT project detected but %s is missing; run "wxt build" and start again to load it`,
			wxtChromeOutput)
	}
	return opts, "", ""
}

// wxtTipWanted suggests --local only when auto-scoping is about to put this
// WXT project's browser state in the global session — never second-guessing
// an explicit --local/--global or a RODDY_HOME override.
func wxtTipWanted(mode scopeMode, roddyHome, stateDir, localDir string) bool {
	return mode == scopeAuto && roddyHome == "" && stateDir != localDir
}

const wxtLocalTip = "tip: use --local to keep this project's browser state isolated (./.roddy/)"
