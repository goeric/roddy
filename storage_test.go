package main

import (
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
		{"--area"},              // missing value
		{"get", "--area", ""},   // an unset shell variable is a mistake, not local
		{"get", "--area=cloud"}, // no such area
		{"get", "--aera", "sync"} /* a typo must not become a key */, {"--ext"},
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
		{"03", `"03"`}, // not JSON, so a string
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
	if got, want := storageSetJS("local", "k", "3"), `chrome.storage.local.set({"k": 3})`; got != want {
		t.Errorf("set: got %s, want %s", got, want)
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
}

func TestStorageGetResult(t *testing.T) {
	present, v := storageGetResult(gson.NewFrom(`[true, 3]`))
	if !present || v.Int() != 3 {
		t.Errorf("got present=%v value=%v, want true 3", present, v)
	}
	if present, _ := storageGetResult(gson.NewFrom(`[false, null]`)); present {
		t.Error("an absent key should not report present")
	}
	// Presence is decided by the key, not the value: a stored null is present.
	present, v = storageGetResult(gson.NewFrom(`[true, null]`))
	if !present || !v.Nil() {
		t.Errorf("got present=%v value=%v, want a present null", present, v)
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
	deadline := time.Now().Add(10 * time.Second)
	for {
		if present, _ := storageGetResult(eval(storageGetJS("local", "seeded"))); present {
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

	if present, v := storageGetResult(eval(storageGetJS("local", "count"))); !present || v.Int() != 3 {
		t.Errorf("get count: got present=%v value=%v", present, v)
	}
	if present, _ := storageGetResult(eval(storageGetJS("local", "nope"))); present {
		t.Error("get of a missing key reported present")
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
	if present, v := storageGetResult(eval(storageGetJS("session", "tok"))); !present || v.Str() != "abc" {
		t.Errorf("session get: got present=%v value=%v", present, v)
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
