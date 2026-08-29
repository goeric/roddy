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
		{"cts config", "wxt.config.cts", true, wxtBuilt},
		{"cjs config", "wxt.config.cjs", true, wxtBuilt},
		{"unrelated config name", "vite.config.ts", true, wxtNone},
	}
	for _, c := range cases {
		if got := detectWXT(writeWXTProject(t, c.config, c.built)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// A build output alone is not a WXT project — it could be any unpacked
	// extension someone copied in.
	if got := detectWXT(writeWXTProject(t, "", true)); got != wxtNone {
		t.Errorf("output without config: got %v, want wxtNone", got)
	}
	// Detection stops at the directory: an output with no manifest is still a
	// candidate here, and wxtStart is what turns it down (see TestWXTStart).
	dir := writeWXTProject(t, "wxt.config.ts", true)
	if err := os.Remove(filepath.Join(dir, ".output", "chrome-mv3", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if got := detectWXT(dir); got != wxtBuilt {
		t.Errorf("output without manifest: got %v, want wxtBuilt", got)
	}
}

func TestWXTStart_UserAlreadyDecided(t *testing.T) {
	built := writeWXTProject(t, "wxt.config.ts", true)
	unbuilt := writeWXTProject(t, "wxt.config.ts", false)
	explicit := startOptions{headless: true, extensions: []string{"./my-ext"}}
	optedOut := startOptions{headless: true, noExtension: true}

	cases := []struct {
		name string
		opts startOptions
		dir  string
	}{
		{"explicit --extension", explicit, built},
		{"explicit --extension, unbuilt", explicit, unbuilt},
		{"--no-extension", optedOut, built},
		{"--no-extension, unbuilt", optedOut, unbuilt},
	}
	for _, c := range cases {
		out, notice, hint := wxtStart(c.opts, c.dir, t.TempDir())
		if notice != "" || hint != "" {
			t.Errorf("%s: got notice %q hint %q, want silence", c.name, notice, hint)
		}
		if len(out.extensions) != len(c.opts.extensions) {
			t.Errorf("%s: extensions changed: %v", c.name, out.extensions)
		}
	}
}

func TestWXTStart_NoProject(t *testing.T) {
	base := startOptions{headless: true}
	out, notice, hint := wxtStart(base, writeWXTProject(t, "", false), t.TempDir())
	if notice != "" || hint != "" || len(out.extensions) != 0 {
		t.Errorf("no project: got %v %q %q, want silence", out.extensions, notice, hint)
	}
}

func TestWXTStart_Unbuilt(t *testing.T) {
	base := startOptions{headless: true}
	out, notice, hint := wxtStart(base, writeWXTProject(t, "wxt.config.ts", false), t.TempDir())
	if notice != "" || len(out.extensions) != 0 {
		t.Errorf("unbuilt: got extensions %v notice %q, want neither", out.extensions, notice)
	}
	for _, want := range []string{"WXT project detected", wxtChromeOutput, "wxt build"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not mention %q", hint, want)
		}
	}
}

func TestWXTStart_Built(t *testing.T) {
	base := startOptions{headless: true}
	dir := writeWXTProject(t, "wxt.config.ts", true)
	out, notice, hint := wxtStart(base, dir, t.TempDir())
	if hint != "" {
		t.Errorf("built: unexpected hint %q", hint)
	}
	for _, want := range []string{"WXT project detected", wxtChromeOutput, "--no-extension"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice %q does not mention %q", notice, want)
		}
	}
	if len(out.extensions) != 1 {
		t.Fatalf("built: got extensions %v, want exactly one", out.extensions)
	}
	if _, err := os.Stat(filepath.Join(out.extensions[0], "manifest.json")); err != nil {
		t.Errorf("appended path is not the loadable build output: %v", err)
	}
}

// A build output that exists but cannot be loaded must not reach loadExtensions,
// which would turn a plain "roddy start" into a fatal error.
func TestWXTStart_BrokenBuildDegrades(t *testing.T) {
	base := startOptions{headless: true}

	corrupt := writeWXTProject(t, "wxt.config.ts", true)
	manifest := filepath.Join(corrupt, ".output", "chrome-mv3", "manifest.json")
	if err := os.WriteFile(manifest, []byte("{ not json"), 0644); err != nil {
		t.Fatal(err)
	}

	noManifest := writeWXTProject(t, "wxt.config.ts", true)
	if err := os.Remove(filepath.Join(noManifest, ".output", "chrome-mv3", "manifest.json")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		dir  string
		want string // the underlying resolveExtension failure
	}{
		{"corrupt manifest", corrupt, "invalid manifest.json"},
		{"missing manifest", noManifest, "no manifest.json"},
	}
	for _, c := range cases {
		out, notice, hint := wxtStart(base, c.dir, t.TempDir())
		if notice != "" || len(out.extensions) != 0 {
			t.Errorf("%s: got extensions %v notice %q, want neither", c.name, out.extensions, notice)
		}
		for _, want := range []string{"WXT project detected", wxtChromeOutput, "--no-extension", c.want} {
			if !strings.Contains(hint, want) {
				t.Errorf("%s: hint %q does not mention %q", c.name, hint, want)
			}
		}
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
