package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWXTProject lays out a fake WXT project: a config file and, when built,
// the .output/chrome-mv3 directory with a manifest.
func writeWXTProject(t *testing.T, configName string, built bool) string {
	t.Helper()
	dir := t.TempDir()
	if configName != "" {
		if err := os.WriteFile(filepath.Join(dir, configName), []byte("export default {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if built {
		out := filepath.Join(dir, ".output", "chrome-mv3")
		if err := os.MkdirAll(out, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "manifest.json"), []byte(swTestManifest), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectWXT(t *testing.T) {
	cases := []struct {
		name   string
		config string
		built  bool
		want   wxtDetection
	}{
		{"no config, no output", "", false, wxtNone},
		{"config without a build", "wxt.config.ts", false, wxtUnbuilt},
		{"built project", "wxt.config.ts", true, wxtBuilt},
		{"js config", "wxt.config.js", true, wxtBuilt},
		{"mts config", "wxt.config.mts", true, wxtBuilt},
		{"mjs config", "wxt.config.mjs", true, wxtBuilt},
		{"unrelated config name", "vite.config.ts", true, wxtNone},
	}
	for _, c := range cases {
		if got := detectWXT(writeWXTProject(t, c.config, c.built)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// A build output alone is not a WXT project (it could be any unpacked
	// extension someone copied in), and a half-deleted output with no manifest
	// counts as unbuilt so the auto path never hands Chrome a broken directory.
	dir := writeWXTProject(t, "", true)
	if got := detectWXT(dir); got != wxtNone {
		t.Errorf("output without config: got %v, want wxtNone", got)
	}
	dir = writeWXTProject(t, "wxt.config.ts", true)
	if err := os.Remove(filepath.Join(dir, ".output", "chrome-mv3", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if got := detectWXT(dir); got != wxtUnbuilt {
		t.Errorf("output without manifest: got %v, want wxtUnbuilt", got)
	}
}

func TestWXTAuto(t *testing.T) {
	base := startOptions{headless: true}
	explicit := startOptions{headless: true, extensions: []string{"./my-ext"}}
	optedOut := startOptions{headless: true, noExtension: true}

	path, notice, hint := wxtAuto(base, wxtBuilt)
	if path != wxtChromeOutput {
		t.Errorf("built: got path %q, want %q", path, wxtChromeOutput)
	}
	if !strings.Contains(notice, "WXT project detected") || !strings.Contains(notice, "--no-extension") {
		t.Errorf("notice should name the detection and the opt-out: %q", notice)
	}
	if hint != "" {
		t.Errorf("built: unexpected hint %q", hint)
	}

	path, notice, hint = wxtAuto(base, wxtUnbuilt)
	if path != "" || notice != "" {
		t.Errorf("unbuilt: got path %q notice %q, want neither", path, notice)
	}
	if !strings.Contains(hint, "wxt build") {
		t.Errorf("hint should say how to build: %q", hint)
	}

	if path, notice, hint = wxtAuto(base, wxtNone); path != "" || notice != "" || hint != "" {
		t.Errorf("no project: got %q %q %q, want silence", path, notice, hint)
	}
	// An explicit --extension or --no-extension means the user already decided.
	if path, notice, hint = wxtAuto(explicit, wxtBuilt); path != "" || notice != "" || hint != "" {
		t.Errorf("explicit --extension: got %q %q %q, want silence", path, notice, hint)
	}
	if path, notice, hint = wxtAuto(optedOut, wxtBuilt); path != "" || notice != "" || hint != "" {
		t.Errorf("--no-extension: got %q %q %q, want silence", path, notice, hint)
	}
	if path, notice, hint = wxtAuto(optedOut, wxtUnbuilt); path != "" || notice != "" || hint != "" {
		t.Errorf("--no-extension unbuilt: got %q %q %q, want silence", path, notice, hint)
	}
}

func TestWXTTipWanted(t *testing.T) {
	local := filepath.Join("proj", ".roddy")
	global := filepath.Join("home", ".roddy")

	cases := []struct {
		name      string
		mode      scopeMode
		roddyHome string
		stateDir  string
		want      bool
	}{
		{"auto resolving global", scopeAuto, "", global, true},
		{"auto resolving local", scopeAuto, "", local, false},
		{"explicit --local", scopeLocal, "", local, false},
		{"explicit --global", scopeGlobal, "", global, false},
		{"RODDY_HOME override", scopeAuto, "/custom", global, false},
	}
	for _, c := range cases {
		if got := wxtTipWanted(c.mode, c.roddyHome, c.stateDir, local); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseStartArgs_NoExtension(t *testing.T) {
	opts, err := parseStartArgs([]string{"--no-extension"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.noExtension {
		t.Error("--no-extension was not recorded")
	}

	// Passing both is a contradiction, not a precedence puzzle.
	_, err = parseStartArgs([]string{"--no-extension", "--extension", "./ext"})
	if err == nil {
		t.Fatal("expected an error for --no-extension with --extension")
	}
	for _, want := range []string{"--no-extension", "--extension"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
