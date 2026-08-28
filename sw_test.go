package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

// --- fixtures ---

const swTestManifest = `{
  "manifest_version": 3,
  "name": "SW Test Extension",
  "version": "1.0.0",
  "background": {"service_worker": "sw.js"},
  "permissions": ["storage"]
}`

// The worker sets a global and seeds storage, so tests can verify both plain
// evaluation and that chrome.* APIs resolve inside the worker.
const swTestWorker = `self.SW_PROBE = "alive";
chrome.storage.local.set({ seeded: "from-sw" });`

// writeSWTestExtension creates an unpacked MV3 extension with a service worker.
func writeSWTestExtension(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", swTestManifest)
	write("sw.js", swTestWorker)
	return dir
}

// launchWithSWExtension starts a headless browser with the fixture extension
// loaded, the same way "roddy start --extension" does.
func launchWithSWExtension(t *testing.T) (*rod.Browser, string) {
	t.Helper()
	dir := writeSWTestExtension(t, filepath.Join(t.TempDir(), "swext"))
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	l := configureExtensions(baseLauncher().Headless(true), true,
		[]extensionInfo{{Dir: dir}})
	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}
	browser := rod.New().ControlURL(l.MustLaunch()).MustConnect()
	t.Cleanup(func() { browser.MustClose() })
	return browser, extensionID(dir)
}

// --- argument handling (no browser) ---

func TestParseSWFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ext     string
		timeout time.Duration
		rest    []string
	}{
		{"no args", nil, "", 5 * time.Second, nil},
		{"flags after the expression", []string{"eval", "1 + 1", "--ext", "abc"},
			"abc", 5 * time.Second, []string{"eval", "1 + 1"}},
		{"flags before the subcommand", []string{"--ext", "abc", "eval", "1 + 1"},
			"abc", 5 * time.Second, []string{"eval", "1 + 1"}},
		{"equals form", []string{"eval", "--ext=abc", "--timeout=10s", "1 + 1"},
			"abc", 10 * time.Second, []string{"eval", "1 + 1"}},
		{"expression starting with a dash", []string{"eval", "-1 + 2"},
			"", 5 * time.Second, []string{"eval", "-1 + 2"}},
		{"zero timeout", []string{"--timeout", "0"}, "", 0, nil},
	}
	for _, c := range cases {
		ext, timeout, rest, err := parseSWFlags(c.args)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if ext != c.ext || timeout != c.timeout {
			t.Errorf("%s: got ext %q timeout %v, want %q %v", c.name, ext, timeout, c.ext, c.timeout)
		}
		if strings.Join(rest, "\x00") != strings.Join(c.rest, "\x00") {
			t.Errorf("%s: got rest %q, want %q", c.name, rest, c.rest)
		}
	}

	for _, args := range [][]string{{"eval", "--ext"}, {"--timeout", "soon"}} {
		if _, _, _, err := parseSWFlags(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestCheckExtensionFilter(t *testing.T) {
	two := []extensionInfo{{ID: "aaaa", Name: "First"}, {ID: "bbbb", Name: "Second"}}
	one := two[:1]

	// Several loaded and no --ext: eval must not guess.
	err := checkExtensionFilter(two, "", true)
	if err == nil {
		t.Fatal("expected an error when several extensions are loaded and --ext is missing")
	}
	for _, want := range []string{"aaaa", "bbbb", "First", "Second", "--ext"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	// Listing is happy to show them all.
	if err := checkExtensionFilter(two, "", false); err != nil {
		t.Errorf("list mode should accept an omitted --ext: %v", err)
	}
	// A single extension needs no choice.
	if err := checkExtensionFilter(one, "", true); err != nil {
		t.Errorf("one loaded extension should need no --ext: %v", err)
	}
	// --ext naming a loaded extension is fine either way.
	for _, mustChoose := range []bool{true, false} {
		if err := checkExtensionFilter(two, "bbbb", mustChoose); err != nil {
			t.Errorf("--ext bbbb (mustChoose=%v): %v", mustChoose, err)
		}
	}
	// --ext naming something else is not.
	for _, mustChoose := range []bool{true, false} {
		err := checkExtensionFilter(two, "cccc", mustChoose)
		if err == nil {
			t.Fatalf("--ext cccc (mustChoose=%v): expected an error", mustChoose)
		}
		if !strings.Contains(err.Error(), "not loaded") || !strings.Contains(err.Error(), "aaaa") {
			t.Errorf("unexpected error: %v", err)
		}
	}
	// A "roddy connect" session records no extensions, so nothing is checked.
	for _, extID := range []string{"", "cccc"} {
		for _, mustChoose := range []bool{true, false} {
			if err := checkExtensionFilter(nil, extID, mustChoose); err != nil {
				t.Errorf("empty state (ext %q, mustChoose=%v): %v", extID, mustChoose, err)
			}
		}
	}
}

func TestExtensionIDFromURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://example.com/sw.js", ""},
		{"chrome-extension://abcdefghijklmnopabcdefghijklmnop/sw.js", "abcdefghijklmnopabcdefghijklmnop"},
		{"chrome-extension://abcdefghijklmnopabcdefghijklmnop", "abcdefghijklmnopabcdefghijklmnop"},
		{"", ""},
	}
	for _, c := range cases {
		if got := extensionIDFromURL(c.url); got != c.want {
			t.Errorf("extensionIDFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// --- listServiceWorkers / waitServiceWorker ---

func TestWaitServiceWorker_FindsExtensionWorker(t *testing.T) {
	browser, extID := launchWithSWExtension(t)

	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sw.ExtensionID != extID {
		t.Errorf("got extension id %q, want %q", sw.ExtensionID, extID)
	}
	want := "chrome-extension://" + extID + "/sw.js"
	if sw.URL != want {
		t.Errorf("got url %q, want %q", sw.URL, want)
	}
}

func TestWaitServiceWorker_FilterByExtensionID(t *testing.T) {
	browser, extID := launchWithSWExtension(t)

	if _, err := waitServiceWorker(browser, extID, 15*time.Second); err != nil {
		t.Errorf("filter matching the loaded extension failed: %v", err)
	}

	_, err := waitServiceWorker(browser, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2*time.Second)
	if err == nil {
		t.Error("expected an error for a filter matching no extension")
	}
}

func TestWaitServiceWorker_TimesOutWithoutExtensions(t *testing.T) {
	// The shared test browser has no extensions, so no worker ever appears.
	start := time.Now()
	_, err := waitServiceWorker(env.browser, "", 2*time.Second)
	if err == nil {
		t.Fatal("expected an error when no service worker exists")
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("returned after %v, before the timeout elapsed", elapsed)
	}
	if !strings.Contains(err.Error(), "no extension service worker") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListServiceWorkers_IgnoresPageServiceWorkers(t *testing.T) {
	browser, extID := launchWithSWExtension(t)

	page := browser.MustPage(env.server.URL + "/sw-page").Timeout(30 * time.Second)
	defer page.MustClose()
	// navigator.serviceWorker.ready resolves once the page's own worker is
	// active, so the target exists by the time the filter is checked.
	if _, err := page.Evaluate(rod.Eval(`() => navigator.serviceWorker.ready.then(() => true)`).ByPromise()); err != nil {
		t.Fatalf("the page's service worker never became ready: %v", err)
	}

	workers, err := waitServiceWorkers(browser, "", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("got %d workers, want only the extension's: %+v", len(workers), workers)
	}
	if workers[0].ExtensionID != extID {
		t.Errorf("got worker %+v, want the extension %s", workers[0], extID)
	}
}

// --- evalInServiceWorker ---

func TestEvalInServiceWorker(t *testing.T) {
	browser, _ := launchWithSWExtension(t)

	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}

	cases := []struct {
		expr string
		want string
	}{
		// A global set by the worker itself proves we are in its context.
		{`self.SW_PROBE`, `"alive"`},
		// chrome.storage only exists inside the extension; an ordinary page has
		// a window.chrome, but not this.
		{`typeof chrome.storage`, `"object"`},
		// Promises must be awaited, and storage round-trips.
		{`chrome.storage.local.get("seeded").then(v => v.seeded)`, `"from-sw"`},
		// An object literal must not be read as a labelled block, matching js.
		{`{a: 1}`, `{"a":1}`},
		// NaN has no JSON encoding; Chrome sends its spelling instead of a value.
		{`0/0`, `"NaN"`},
	}
	for _, c := range cases {
		got, err := evalInServiceWorker(browser, sw, c.expr)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.expr, err)
			continue
		}
		if raw := got.JSON("", ""); raw != c.want {
			t.Errorf("%s = %s, want %s", c.expr, raw, c.want)
		}
	}

	// The unserializable result must print bare, not as null.
	got, err := evalInServiceWorker(browser, sw, `0/0`)
	if err != nil {
		t.Fatalf("0/0: unexpected error: %v", err)
	}
	if formatted := formatJSValue(got); formatted != "NaN" {
		t.Errorf("0/0 prints as %q, want %q", formatted, "NaN")
	}
}

func TestEvalInServiceWorker_SurfacesExceptions(t *testing.T) {
	browser, _ := launchWithSWExtension(t)

	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}

	_, err = evalInServiceWorker(browser, sw, `nonexistentFunction()`)
	if err == nil {
		t.Fatal("expected a thrown expression to return an error")
	}
	if !strings.Contains(err.Error(), "nonexistentFunction") {
		t.Errorf("error does not mention the failing expression: %v", err)
	}

	// A rejected primitive has no Description, only a Value; without it the
	// message would be a bare "Uncaught (in promise)".
	_, err = evalInServiceWorker(browser, sw, `Promise.reject("storage not initialized")`)
	if err == nil {
		t.Fatal("expected a rejected promise to return an error")
	}
	if !strings.Contains(err.Error(), "storage not initialized") {
		t.Errorf("error does not carry the rejection reason: %v", err)
	}
}

func TestEvalInServiceWorker_TimesOut(t *testing.T) {
	browser, _ := launchWithSWExtension(t)

	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}

	restore := defaultTimeout
	defaultTimeout = 2 * time.Second
	t.Cleanup(func() { defaultTimeout = restore })

	start := time.Now()
	_, err = evalInServiceWorker(browser, sw, `new Promise(() => {})`)
	if err == nil {
		t.Fatal("expected a promise that never settles to time out")
	}
	if !strings.Contains(err.Error(), "evaluation timed out") {
		t.Errorf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "no extension service worker") {
		t.Errorf("the evaluation timeout must read differently from the lookup one: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 15*time.Second {
		t.Errorf("returned after %v, want about %v", elapsed, defaultTimeout)
	}
}
