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
		// chrome.* APIs only exist inside the extension, never on a web page.
		{`typeof chrome.storage`, `"object"`},
		// Promises must be awaited, and storage round-trips.
		{`chrome.storage.local.get("seeded").then(v => v.seeded)`, `"from-sw"`},
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
}
