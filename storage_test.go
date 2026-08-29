package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ysmood/gson"
)

// --- argument handling (no browser) ---

func TestParseStorageFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		area    string
		ext     string
		timeout time.Duration
		rest    []string
	}{
		{"defaults", nil, "local", "", 5 * time.Second, nil},
		{"area after the subcommand", []string{"get", "--area", "sync"},
			"sync", "", 5 * time.Second, []string{"get"}},
		{"equals form", []string{"--area=session", "get", "k"},
			"session", "", 5 * time.Second, []string{"get", "k"}},
		{"managed area", []string{"get", "--area", "managed"},
			"managed", "", 5 * time.Second, []string{"get"}},
		{"ext and timeout pass through", []string{"set", "k", "v", "--ext", "abc", "--timeout", "10s"},
			"local", "abc", 10 * time.Second, []string{"set", "k", "v"}},
		{"negative number value is not a flag", []string{"set", "count", "-1.5"},
			"local", "", 5 * time.Second, []string{"set", "count", "-1.5"}},
		{"double dash protects a flag-like key", []string{"get", "--", "--area"},
			"local", "", 5 * time.Second, []string{"get", "--area"}},
	}
	for _, c := range cases {
		area, ext, timeout, rest, err := parseStorageFlags(c.args)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if area != c.area || ext != c.ext || timeout != c.timeout {
			t.Errorf("%s: got area %q ext %q timeout %v, want %q %q %v",
				c.name, area, ext, timeout, c.area, c.ext, c.timeout)
		}
		if strings.Join(rest, "\x00") != strings.Join(c.rest, "\x00") {
			t.Errorf("%s: got rest %q, want %q", c.name, rest, c.rest)
		}
	}

	bad := [][]string{
		{"--area"},                // missing value
		{"get", "--area", ""},     // an unset shell variable is a mistake, not local
		{"get", "--area=cloud"},   // no such area
		{"get", "--aera", "sync"}, // a typo must not become a key
		{"--ext"},
		{"get", "--ext", ""},          // as for --area, empty is a mistake
		{"get", "--ext", "--timeout"}, // a missing ID must not swallow the next flag
		{"get", "--timeout", "bogus"}, // not a duration
	}
	for _, args := range bad {
		if _, _, _, _, err := parseStorageFlags(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}

	// The typo error names the offending token, not a generic parse failure.
	_, _, _, _, err := parseStorageFlags([]string{"get", "--aera", "sync"})
	if err == nil || !strings.Contains(err.Error(), "--aera") {
		t.Errorf("unknown flag error should name the flag: %v", err)
	}
}

func TestStorageArgs(t *testing.T) {
	cases := []struct {
		name  string
		rest  []string
		verb  string
		key   string
		value string
		keys  []string
	}{
		{"get whole area", []string{"get"}, "get", "", "", nil},
		{"get one key", []string{"get", "k"}, "get", "k", "", nil},
		{"set", []string{"set", "k", "v"}, "set", "k", "v", nil},
		{"rm one key", []string{"rm", "a"}, "rm", "", "", []string{"a"}},
		{"rm several keys", []string{"rm", "a", "b"}, "rm", "", "", []string{"a", "b"}},
		{"clear", []string{"clear"}, "clear", "", "", nil},
	}
	for _, c := range cases {
		op, err := storageArgs(c.rest)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if op.verb != c.verb || op.key != c.key || op.value != c.value {
			t.Errorf("%s: got %+v, want verb %q key %q value %q", c.name, op, c.verb, c.key, c.value)
		}
		if strings.Join(op.keys, "\x00") != strings.Join(c.keys, "\x00") {
			t.Errorf("%s: got keys %q, want %q", c.name, op.keys, c.keys)
		}
	}

	bad := [][]string{
		{},                     // no subcommand
		{"get", "a", "b"},      // get takes at most one key
		{"set"},                // set takes exactly KEY VALUE
		{"set", "k"},           //
		{"set", "k", "v", "w"}, //
		{"rm"},                 // rm needs at least one key
		{"clear", "x"},         // clear takes nothing
		{"frob"},               // unknown subcommand
		// An unset shell variable must not turn "get $KEY" into a whole-area dump
		// that exits 0, nor write or remove a key named "".
		{"get", ""},
		{"set", "", "v"},
		{"rm", ""},
		{"rm", "a", ""},
	}
	for _, rest := range bad {
		if _, err := storageArgs(rest); err == nil {
			t.Errorf("%v: expected an error", rest)
		}
	}
}

// --- JS generation (no browser) ---

func TestStorageValueJS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"3", "3"},
		{"true", "true"},
		{"-1.5", "-1.5"},
		{"null", "null"},
		{`{"a":1}`, `{"a":1}`},
		{"[1,2]", "[1,2]"},
		{`"true"`, `"true"`}, // pre-quoted: the string "true", not the boolean
		{"test", `"test"`},
		{"3abc", `"3abc"`},
		{"03", `"03"`},   // not JSON, so a string
		{" 3", " 3"},     // JSON tolerates leading space, so this is still the number
		{"-.5", `"-.5"`}, // not flag-like, but not JSON either
		{`he said "hi"`, `"he said \"hi\""`},
		{"", `""`},
	}
	for _, c := range cases {
		if got := storageValueJS(c.in); got != c.want {
			t.Errorf("storageValueJS(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestStorageJSBuilders(t *testing.T) {
	if got, want := storageGetAllJS("local"), `chrome.storage.local.get(null)`; got != want {
		t.Errorf("getAll: got %s, want %s", got, want)
	}
	if got, want := storageGetJS("sync", "k"),
		`chrome.storage.sync.get(["k"]).then(r => [Object.hasOwn(r, "k"), r["k"]])`; got != want {
		t.Errorf("get: got %s, want %s", got, want)
	}
	if got, want := storageSetJS("local", "k", "3"),
		`chrome.storage.local.set(JSON.parse("{\"k\":3}"))`; got != want {
		t.Errorf("set: got %s, want %s", got, want)
	}
	// An object literal would make "__proto__" set the prototype and store
	// nothing; JSON.parse gives it an own property, at any depth.
	if got, want := storageSetJS("local", "__proto__", `{"__proto__":1}`),
		`chrome.storage.local.set(JSON.parse("{\"__proto__\":{\"__proto__\":1}}"))`; got != want {
		t.Errorf("set __proto__: got %s, want %s", got, want)
	}
	if got, want := storageRemoveJS("session", []string{"a", "b"}),
		`chrome.storage.session.remove(["a","b"])`; got != want {
		t.Errorf("remove: got %s, want %s", got, want)
	}
	if got, want := storageClearJS("local"), `chrome.storage.local.clear()`; got != want {
		t.Errorf("clear: got %s, want %s", got, want)
	}
	// Keys are JSON-encoded wherever they land, so quotes cannot escape into JS.
	hostile := storageGetJS("local", `we"ird`)
	if want := `chrome.storage.local.get(["we\"ird"]).then(r => [Object.hasOwn(r, "we\"ird"), r["we\"ird"]])`; hostile != want {
		t.Errorf("hostile key: got %s, want %s", hostile, want)
	}
	if got, want := storageGuardJS("EXPR"),
		`(globalThis.chrome?.storage ? (EXPR) : Promise.reject(Error("chrome.storage is unavailable: the extension must declare the \"storage\" permission in its manifest")))`; got != want {
		t.Errorf("guard: got %s, want %s", got, want)
	}
}

func TestStorageGetResult(t *testing.T) {
	present, v, err := storageGetResult(gson.NewFrom(`[true, 3]`))
	if err != nil || !present || v.Int() != 3 {
		t.Errorf("got present=%v value=%v err=%v, want true 3", present, v, err)
	}
	if present, _, err := storageGetResult(gson.NewFrom(`[false, null]`)); present || err != nil {
		t.Errorf("an absent key should report present=false, no error: %v %v", present, err)
	}
	// Presence is decided by the key, not the value: a stored null is present.
	present, v, err = storageGetResult(gson.NewFrom(`[true, null]`))
	if err != nil || !present || !v.Nil() {
		t.Errorf("got present=%v value=%v err=%v, want a present null", present, v, err)
	}
	// A pair is an invariant of our own generated JS: if it ever breaks, say so
	// rather than answering "not present".
	for _, bad := range []string{`null`, `[true]`, `[true, 3, 4]`, `{"a":1}`} {
		if _, _, err := storageGetResult(gson.NewFrom(bad)); err == nil {
			t.Errorf("%s: expected an error", bad)
		} else if !strings.Contains(err.Error(), "unexpected storage get result") {
			t.Errorf("%s: unhelpful error: %v", bad, err)
		}
	}
}

// --- end to end against a real worker ---

func TestStorage_EndToEnd(t *testing.T) {
	browser, _ := launchWithSWExtension(t)
	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}
	eval := func(js string) gson.JSON {
		t.Helper()
		v, err := evalInServiceWorker(browser, sw, js)
		if err != nil {
			t.Fatalf("%s: %v", js, err)
		}
		return v
	}

	// The fixture worker seeds local storage at boot; wait for the seed so the
	// clear below cannot race it and leak "seeded" into the counts.
	get := func(area, key string) (bool, gson.JSON) {
		t.Helper()
		present, v, err := storageGetResult(eval(storageGetJS(area, key)))
		if err != nil {
			t.Fatalf("get %s %s: %v", area, key, err)
		}
		return present, v
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if present, _ := get("local", "seeded"); present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fixture worker never seeded local storage")
		}
		time.Sleep(100 * time.Millisecond)
	}
	eval(storageClearJS("local"))

	// Values arrive typed, through the same JSON-else-string parse the CLI uses.
	for _, kv := range [][2]string{
		{"count", "3"}, {"flag", "true"}, {"user", "test"}, {"config", `{"theme":"dark"}`},
	} {
		eval(storageSetJS("local", kv[0], storageValueJS(kv[1])))
	}
	all := eval(storageGetAllJS("local")).Map()
	if len(all) != 4 {
		t.Fatalf("got %d keys, want 4: %v", len(all), all)
	}
	if all["count"].Int() != 3 || all["flag"].Bool() != true ||
		all["user"].Str() != "test" || all["config"].Get("theme").Str() != "dark" {
		t.Errorf("stored values came back wrong: %v", all)
	}

	if present, v := get("local", "count"); !present || v.Int() != 3 {
		t.Errorf("get count: got present=%v value=%v", present, v)
	}
	if present, _ := get("local", "nope"); present {
		t.Error("get of a missing key reported present")
	}

	// "__proto__" survives as an own property: an object literal would have set
	// the prototype and stored nothing while still reporting success.
	eval(storageSetJS("local", "__proto__", storageValueJS("polluted")))
	if present, v := get("local", "__proto__"); !present || v.Str() != "polluted" {
		t.Errorf("get __proto__: got present=%v value=%v", present, v)
	}
	if all := eval(storageGetAllJS("local")).Map(); all["__proto__"].Str() != "polluted" {
		t.Errorf("whole-area get lost __proto__: %v", all)
	}
	eval(storageRemoveJS("local", []string{"__proto__"}))
	if present, _ := get("local", "__proto__"); present {
		t.Error("__proto__ survived rm")
	}

	// remove ignores keys that are not there, matching Chrome's own semantics.
	eval(storageRemoveJS("local", []string{"count", "nope"}))
	if all := eval(storageGetAllJS("local")).Map(); len(all) != 3 {
		t.Errorf("after rm: got %d keys, want 3: %v", len(all), all)
	}
	eval(storageClearJS("local"))
	if all := eval(storageGetAllJS("local")).Map(); len(all) != 0 {
		t.Errorf("after clear: got %d keys, want 0: %v", len(all), all)
	}

	// The session area is reachable from the worker's trusted context.
	eval(storageSetJS("session", "tok", storageValueJS("abc")))
	if present, v := get("session", "tok"); !present || v.Str() != "abc" {
		t.Errorf("session get: got present=%v value=%v", present, v)
	}

	// sync works without a signed-in profile, backed by local storage.
	eval(storageSetJS("sync", "theme", storageValueJS(`{"dark":true}`)))
	if present, v := get("sync", "theme"); !present || !v.Get("dark").Bool() {
		t.Errorf("sync get: got present=%v value=%v", present, v)
	}

	// managed reads as empty without an enterprise policy, and writes surface
	// Chrome's own error rather than anything of ours.
	if all := eval(storageGetAllJS("managed")).Map(); len(all) != 0 {
		t.Errorf("managed area unexpectedly has keys: %v", all)
	}
	_, err = evalInServiceWorker(browser, sw, storageSetJS("managed", "x", "1"))
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("managed set: got %v, want Chrome's read-only error", err)
	}
}

// An extension whose manifest declares no permissions: chrome.storage is
// undefined inside its worker.
const noPermsManifest = `{
  "manifest_version": 3,
  "name": "No Permissions Extension",
  "version": "1.0.0",
  "background": {"service_worker": "sw.js"}
}`

func TestStorage_MissingStoragePermission(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nopermsext")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"manifest.json": noPermsManifest,
		"sw.js":         `self.SW_PROBE = "alive";`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	browser, _ := launchWithExtensions(t, dir)
	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}
	// The guard names the missing permission; without it every subcommand dies
	// with "TypeError: Cannot read properties of undefined (reading 'local')".
	_, err = evalInServiceWorker(browser, sw, storageGuardJS(storageGetAllJS("local")))
	if err == nil || !strings.Contains(err.Error(), `must declare the "storage" permission`) {
		t.Errorf("got %v, want the missing-permission message", err)
	}
}
