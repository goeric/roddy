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

// launchWithExtensions starts a headless browser with the given unpacked
// extension directories loaded, the same way "roddy start --extension" does,
// and returns the state entries roddy would have recorded for them.
func launchWithExtensions(t *testing.T, dirs ...string) (*rod.Browser, []extensionInfo) {
	t.Helper()
	infos := make([]extensionInfo, len(dirs))
	for i, dir := range dirs {
		info, err := resolveExtension(dir, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		infos[i] = info
	}

	l := configureExtensions(baseLauncher().Headless(true), true, infos)
	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}
	browser := rod.New().ControlURL(l.MustLaunch()).MustConnect()
	t.Cleanup(func() { browser.MustClose() })
	return browser, infos
}

// launchWithSWExtension starts a browser with only the service-worker fixture.
func launchWithSWExtension(t *testing.T) (*rod.Browser, string) {
	t.Helper()
	browser, infos := launchWithExtensions(t,
		writeSWTestExtension(t, filepath.Join(t.TempDir(), "swext")))
	return browser, infos[0].ID
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
		{"double dash ends the flags", []string{"eval", "--ext", "abc", "--", "--x"},
			"abc", 5 * time.Second, []string{"eval", "--x"}},
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

	// An empty --ext is a mistake, not "no filter": it is what an unset shell
	// variable expands to.
	bad := [][]string{
		{"eval", "--ext"},
		{"--timeout", "soon"},
		{"eval", "--ext", "", "1"},
		{"eval", "--ext=", "1"},
	}
	for _, args := range bad {
		if _, _, _, err := parseSWFlags(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestSWCandidates(t *testing.T) {
	worker := extensionInfo{ID: "aaaa", Name: "Worker", HasWorker: true}
	worker2 := extensionInfo{ID: "bbbb", Name: "Worker Two", HasWorker: true}
	plain := extensionInfo{ID: "cccc", Name: "Content Only"}

	cases := []struct {
		name  string
		exts  []extensionInfo
		want  int
		known bool
	}{
		{"nothing loaded", nil, 0, false},
		{"one worker, one content script", []extensionInfo{worker, plain}, 1, true},
		{"two workers", []extensionInfo{worker, worker2}, 2, true},
		{"content scripts only", []extensionInfo{plain}, 1, false},
		// State written before roddy recorded the flag: nothing is known, so
		// every extension stays a candidate and the old counting applies.
		{"legacy state", []extensionInfo{{ID: "aaaa"}, {ID: "bbbb"}}, 2, false},
	}
	for _, c := range cases {
		candidates, known := swCandidates(c.exts)
		if len(candidates) != c.want || known != c.known {
			t.Errorf("%s: got %d candidates (known=%v), want %d (known=%v)",
				c.name, len(candidates), known, c.want, c.known)
		}
	}
}

func TestCheckExtensionFilter(t *testing.T) {
	// Neither of these declares a worker, so this doubles as the compatibility
	// case: state written before roddy recorded the flag must behave as before.
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

func TestCheckExtensionFilter_ContentScriptOnly(t *testing.T) {
	mixed := []extensionInfo{
		{ID: "aaaa", Name: "Worker", HasWorker: true},
		{ID: "cccc", Name: "Content Only"},
	}

	// Only one of the two can ever have a worker, so eval needs no --ext.
	if err := checkExtensionFilter(mixed, "", true); err != nil {
		t.Errorf("eval should not need --ext when only one extension can have a worker: %v", err)
	}
	// Filtering to the one that cannot is a mistake of its own, told apart from
	// naming an extension that is not loaded at all.
	err := checkExtensionFilter(mixed, "cccc", true)
	if err == nil {
		t.Fatal("expected an error for an extension with no service worker")
	}
	if !strings.Contains(err.Error(), "no service worker") || strings.Contains(err.Error(), "not loaded") {
		t.Errorf("unexpected error: %v", err)
	}
	// Two worker-capable extensions still force a choice, and only they are
	// offered as candidates.
	both := append(append([]extensionInfo{}, mixed...),
		extensionInfo{ID: "bbbb", Name: "Worker Two", HasWorker: true})
	err = checkExtensionFilter(both, "", true)
	if err == nil {
		t.Fatal("expected an error when two extensions could have a worker")
	}
	if strings.Contains(err.Error(), "cccc") {
		t.Errorf("the content-script-only extension is not a candidate: %v", err)
	}
}

func TestResolveExtension_RecordsServiceWorker(t *testing.T) {
	sw, err := resolveExtension(writeSWTestExtension(t, filepath.Join(t.TempDir(), "swext")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !sw.HasWorker {
		t.Error("an extension declaring background.service_worker should report HasWorker")
	}
	// extensions_test.go's fixture is content-script-only.
	plain, err := resolveExtension(writeTestExtension(t, filepath.Join(t.TempDir(), "plain")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if plain.HasWorker {
		t.Error("a content-script-only extension should not report HasWorker")
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

func TestSW_ContentScriptOnlyExtensionIsNotAWorkerCandidate(t *testing.T) {
	tmp := t.TempDir()
	browser, infos := launchWithExtensions(t,
		writeSWTestExtension(t, filepath.Join(tmp, "swext")),
		writeTestExtension(t, filepath.Join(tmp, "plain")))

	candidates, known := swCandidates(infos)
	if !known || len(candidates) != 1 {
		t.Fatalf("got %d worker candidates (known=%v), want 1", len(candidates), known)
	}
	// eval needs no --ext: the second extension can never own a worker.
	if err := checkExtensionFilter(infos, "", true); err != nil {
		t.Errorf("eval should not need --ext here: %v", err)
	}

	// Listing must not sit out the whole timeout waiting for a second worker.
	start := time.Now()
	workers, err := waitServiceWorkers(browser, "", len(candidates), 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(workers) != 1 || workers[0].ExtensionID != infos[0].ID {
		t.Fatalf("got %+v, want only the service-worker extension %s", workers, infos[0].ID)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("listing took %v, close to the full timeout", elapsed)
	}

	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}
	got, err := evalInServiceWorker(browser, sw, `self.SW_PROBE`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw := got.JSON("", ""); raw != `"alive"` {
		t.Errorf("evaluated in the wrong context: got %s", raw)
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

	// null has neither a Description nor a Value, so the reason comes from the
	// exception object's subtype.
	_, err = evalInServiceWorker(browser, sw, `Promise.reject(null)`)
	if err == nil {
		t.Fatal("expected a rejected promise to return an error")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Errorf("error does not say what was rejected: %v", err)
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
