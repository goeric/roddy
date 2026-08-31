package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// --- glob (no browser) ---

// The dialect is Playwright's, ported from packages/isomorphic/urlMatch.ts:
// '*' stays inside a path segment, '**' crosses them, '{a,b}' alternates,
// '?' is a literal, backslash escapes. These cases pin that grammar.
func TestGlobToRegexPattern(t *testing.T) {
	cases := []struct {
		glob  string
		url   string
		match bool
	}{
		{"**/*.js", "https://x.com/a/b.js", true},
		{"**/*.js", "https://x.com/a.js", true},
		{"**/*.js", "https://x.com/a/b.css", false},
		{"**/*.js", "https://x.com/a/b.jsx", false},
		// single '*' does not cross a segment
		{"https://x.com/*.js", "https://x.com/a.js", true},
		{"https://x.com/*.js", "https://x.com/a/b.js", false},
		// '**' between slashes spans zero or more whole segments
		{"https://x.com/a/**/b", "https://x.com/a/b", true},
		{"https://x.com/a/**/b", "https://x.com/a/c/b", true},
		{"https://x.com/a/**/b", "https://x.com/a/c/d/b", true},
		{"https://x.com/a/**/b", "https://x.com/a/cb", false},
		// '**' not followed by '/' spans anything
		{"**/api/**", "https://h.io/api/x", true},
		{"**/api/**", "https://h.io/v1/api/", true},
		{"**/api/**", "https://h.io/apix", false},
		// alternation
		{"https://x.com/{a,b}.txt", "https://x.com/a.txt", true},
		{"https://x.com/{a,b}.txt", "https://x.com/b.txt", true},
		{"https://x.com/{a,b}.txt", "https://x.com/c.txt", false},
		// '?' is a literal, not a wildcard
		{"https://x.com/p?q=1", "https://x.com/p?q=1", true},
		{"https://x.com/p?q=1", "https://x.com/pXq=1", false},
		// escaped star is a literal star
		{`https://x.com/a\*b`, "https://x.com/a*b", true},
		{`https://x.com/a\*b`, "https://x.com/aXb", false},
		// comma outside a group is a literal
		{"https://x.com/a,b", "https://x.com/a,b", true},
		// a trailing backslash escapes nothing and stands for itself
		{`https://x.com/a\`, `https://x.com/a\`, true},
		// regexp metacharacters in the glob are literals
		{"**/q=a+b", "https://h/q=a+b", true},
		{"**/q=a+b", "https://h/q=aab", false},
		// matching is case-sensitive, as Playwright's is
		{"**/API/**", "https://h.io/api/x", false},
		{"**/API/**", "https://h.io/API/x", true},
		// full-match anchoring
		{"https://x.com/a", "https://x.com/a/b", false},
	}
	for _, c := range cases {
		re, err := compileGlob(c.glob)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.glob, err)
			continue
		}
		if got := re.MatchString(c.url); got != c.match {
			t.Errorf("glob %s vs %s: got %v, want %v", c.glob, c.url, got, c.match)
		}
	}

	for _, bad := range []string{"{a,{b,c}}", "a}b", "{a,b"} {
		if _, err := compileGlob(bad); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

// --- rules file (no browser) ---

func writeRules(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadStubRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.json"), []byte(`{"fixture":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rules.json")
	body := `[
		{"url": "**/api/user", "fulfill": {"json": {"name": "test"}}},
		{"url": "**/api/list", "method": "get", "fulfill": {"status": 500, "body": "boom", "contentType": "text/plain"}},
		{"url": "**/fixture", "fulfill": {"path": "user.json"}},
		{"url": "**/telemetry/**", "abort": "internetdisconnected"},
		{"url": "**/quick", "abort": true},
		{"url": "**/real/**", "continue": true}
	]`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	rules, err := loadStubRules(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 6 {
		t.Fatalf("got %d rules, want 6", len(rules))
	}
	if rules[0].fulfill == nil || string(rules[0].fulfill.Body) != `{"name":"test"}` {
		t.Errorf("rule 1: json body wrong: %+v", rules[0].fulfill)
	}
	if got := headerEntry(rules[0].fulfill.ResponseHeaders, "Content-Type"); got != "application/json" {
		t.Errorf("rule 1: json implies application/json, got %q", got)
	}
	if rules[1].method != "GET" {
		t.Errorf("rule 2: method not normalized: %q", rules[1].method)
	}
	if rules[1].fulfill.ResponseCode != 500 {
		t.Errorf("rule 2: status not applied: %d", rules[1].fulfill.ResponseCode)
	}
	if got := headerEntry(rules[1].fulfill.ResponseHeaders, "Content-Type"); got != "text/plain" {
		t.Errorf("rule 2: contentType not applied: %q", got)
	}
	// path fixtures are read at load time, relative to the rules file
	if string(rules[2].fulfill.Body) != `{"fixture":true}` {
		t.Errorf("rule 3: fixture body wrong: %q", rules[2].fulfill.Body)
	}
	if rules[3].abort != proto.NetworkErrorReasonInternetDisconnected {
		t.Errorf("rule 4: abort reason wrong: %q", rules[3].abort)
	}
	if rules[4].abort != proto.NetworkErrorReasonFailed {
		t.Errorf("rule 5: abort true should mean failed: %q", rules[4].abort)
	}

	// A bodyless fulfill still carries a zero-length body: rod marshals a nil
	// Body as JSON null and Chrome rejects that with "binary value expected".
	bodyless, err := loadStubRules(writeRules(t, `[{"url": "**", "fulfill": {"status": 204}}]`))
	if err != nil {
		t.Fatalf("bodyless fulfill: %v", err)
	}
	if b := bodyless[0].fulfill.Body; b == nil || len(b) != 0 {
		t.Errorf("bodyless fulfill: got body %#v, want a non-nil empty slice", b)
	}
	if bodyless[0].fulfill.ResponseCode != 204 {
		t.Errorf("bodyless fulfill: got status %d, want 204", bodyless[0].fulfill.ResponseCode)
	}

	// An absolute fixture path is used as written — the rules file here lives
	// in a different directory, so joining would not find it.
	absRules, err := loadStubRules(writeRules(t,
		fmt.Sprintf(`[{"url": "**", "fulfill": {"path": %q}}]`, filepath.Join(dir, "user.json"))))
	if err != nil {
		t.Fatalf("absolute fixture path: %v", err)
	}
	if string(absRules[0].fulfill.Body) != `{"fixture":true}` {
		t.Errorf("absolute fixture path: got body %q", absRules[0].fulfill.Body)
	}

	bad := map[string]string{
		"not an array":     `{"url": "a"}`,
		"no verb":          `[{"url": "**"}]`,
		"two verbs":        `[{"url": "**", "abort": true, "continue": true}]`,
		"unknown key":      `[{"url": "**", "fullfill": {}}]`,
		"missing url":      `[{"abort": true}]`,
		"bad glob":         `[{"url": "{a", "abort": true}]`,
		"bad abort reason": `[{"url": "**", "abort": "wifidown"}]`,
		"two body sources": `[{"url": "**", "fulfill": {"body": "x", "json": 1}}]`,
		"missing fixture":  `[{"url": "**", "fulfill": {"path": "nope.json"}}]`,
		"empty file":       `[]`,
		"continue false":   `[{"url": "**", "continue": false}]`,
		// A status is validated when it is written, so an explicit 0 — which
		// absent would have meant 200 — is a mistake rather than a default.
		"explicit zero status": `[{"url": "**", "fulfill": {"status": 0}}]`,
		"status below 100":     `[{"url": "**", "fulfill": {"status": 99}}]`,
		"status above 599":     `[{"url": "**", "fulfill": {"status": 600}}]`,
		// Chrome would reject these per request with "Invalid header" and the
		// request would fall through to the real network: they fail at load.
		"header name with a space": `[{"url": "**", "fulfill": {"headers": {"X Bad": "1"}}}]`,
		"header value newline":     `[{"url": "**", "fulfill": {"headers": {"X-Bad": "a\nb"}}}]`,
		"contentType newline":      `[{"url": "**", "fulfill": {"contentType": "text/plain\nX-Bad: 1"}}]`,
		// Two concatenated arrays would otherwise load as only the first.
		"trailing json": `[{"url": "**/a", "abort": true}][{"url": "**/b", "abort": true}]`,
	}
	for name, body := range bad {
		if _, err := loadStubRules(writeRules(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// The message has to name the real problem, including which rule has it.
	for _, c := range []struct{ name, body, want string }{
		{"nested brace", `[{"url": "**", "abort": true}, {"url": "{a,{b,c}}", "abort": true}]`, `rule 2: invalid glob pattern "{a,{b,c}}": nested '{'`},
		{"two verbs", `[{"url": "**", "abort": true, "continue": true}]`, `need exactly one of "fulfill", "abort", "continue"`},
		{"bad status", `[{"url": "**", "fulfill": {"status": 600}}]`, "invalid fulfill status 600"},
		// The conflict is reported before the fixture is opened, so a rule with
		// both does not complain about the file it never should have read.
		{"body and path", `[{"url": "**", "fulfill": {"body": "x", "path": "nope.json"}}]`, `fulfill takes at most one of "body", "json", "path"`},
	} {
		_, err := loadStubRules(writeRules(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", c.name, err, c.want)
		}
	}
}

// --- decision (no browser) ---

func pauseEvent(method, url string, headers map[string]string) *proto.FetchRequestPaused {
	h := proto.NetworkHeaders{}
	for k, v := range headers {
		h[k] = gson.New(v)
	}
	return &proto.FetchRequestPaused{
		RequestID: "r1",
		Request:   &proto.NetworkRequest{Method: method, URL: url, Headers: h},
	}
}

func TestStubDecide(t *testing.T) {
	rules, err := loadStubRules(writeRules(t, `[
		{"url": "**/api/user", "method": "POST", "fulfill": {"json": 1}},
		{"url": "**/api/**", "fulfill": {"body": "generic"}},
		{"url": "**/telemetry/**", "abort": true},
		{"url": "**/passthrough", "continue": true},
		{"url": "**/patchy", "method": "PATCH", "fulfill": {"body": "p"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		e    *proto.FetchRequestPaused
		kind stubKind
		rule int
	}{
		{"first match wins", pauseEvent("POST", "https://h/api/user", nil), stubFulfill, 0},
		{"method filter falls through", pauseEvent("GET", "https://h/api/user", nil), stubFulfill, 1},
		{"abort rule", pauseEvent("POST", "https://h/telemetry/x", nil), stubAbort, 2},
		{"explicit continue", pauseEvent("GET", "https://h/passthrough", nil), stubContinue, 3},
		{"unmatched continues", pauseEvent("GET", "https://h/other", nil), stubContinue, -1},
		// A preflight is answered when the request it announces would match a
		// stubbing rule, and left to the real server when it would not.
		{"preflight for stubbed request", pauseEvent("OPTIONS", "https://h/api/user",
			map[string]string{"Access-Control-Request-Method": "POST", "Origin": "https://o"}), stubPreflight, 0},
		{"preflight for unmatched request", pauseEvent("OPTIONS", "https://h/other",
			map[string]string{"Access-Control-Request-Method": "GET"}), stubContinue, -1},
		{"preflight for continue rule", pauseEvent("OPTIONS", "https://h/passthrough",
			map[string]string{"Access-Control-Request-Method": "GET"}), stubContinue, -1},
		{"preflight for an aborted request", pauseEvent("OPTIONS", "https://h/telemetry/x",
			map[string]string{"Access-Control-Request-Method": "POST"}), stubPreflight, 2},
		{"plain OPTIONS is not a preflight", pauseEvent("OPTIONS", "https://h/api/other", nil), stubFulfill, 1},
		// fetch() normalizes only the six common verbs, so "patch" reaches CDP
		// verbatim and still has to match the rule's uppercase method.
		{"lowercase method", pauseEvent("patch", "https://h/patchy", nil), stubFulfill, 4},
		{"lowercase preflight method", pauseEvent("OPTIONS", "https://h/patchy",
			map[string]string{"Access-Control-Request-Method": "patch"}), stubPreflight, 4},
	}
	for _, c := range cases {
		d := stubDecide(rules, c.e)
		if d.kind != c.kind || d.rule != c.rule {
			t.Errorf("%s: got kind=%v rule=%d, want kind=%v rule=%d", c.name, d.kind, d.rule, c.kind, c.rule)
		}
	}

	// A redirect hop is continued without consulting the rules at all.
	hop := pauseEvent("GET", "https://h/api/user", nil)
	hop.RedirectedRequestID = "int-1"
	if d := stubDecide(rules, hop); d.kind != stubContinueHop {
		t.Errorf("redirect hop: got %v, want stubContinueHop", d.kind)
	}
}

func TestStubFulfillCORS(t *testing.T) {
	rules, err := loadStubRules(writeRules(t, `[{"url": "**", "fulfill": {"body": "x"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	// A request carrying an Origin gets it reflected so the stubbed response is
	// readable; a GET without an Origin header gets no CORS headers.
	p := stubFulfillPayload(rules[0], pauseEvent("GET", "https://h/x", map[string]string{"Origin": "https://o"}))
	for name, want := range map[string]string{
		"Access-Control-Allow-Origin":      "https://o",
		"Access-Control-Allow-Credentials": "true",
		"Vary":                             "Origin",
	} {
		if got := headerEntry(p.ResponseHeaders, name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	p = stubFulfillPayload(rules[0], pauseEvent("GET", "https://h/x", nil))
	if got := headerEntry(p.ResponseHeaders, "Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unexpected ACAO on same-origin fulfill: %q", got)
	}

	// A rule that sets ACAO itself is not second-guessed.
	rules, err = loadStubRules(writeRules(t, `[{"url": "**", "fulfill": {"headers": {"Access-Control-Allow-Origin": "*"}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	p = stubFulfillPayload(rules[0], pauseEvent("GET", "https://h/x", map[string]string{"Origin": "https://o"}))
	if got := headerEntry(p.ResponseHeaders, "Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("rule's own ACAO overridden: %q", got)
	}
	// Reflecting into the copy must not accumulate onto the shared template.
	if n := len(rules[0].fulfill.ResponseHeaders); n != 1 {
		t.Errorf("fulfill template mutated: %d headers, want 1", n)
	}
}

// Content-Type has three sources; the more explicit one wins.
func TestStubFulfillContentType(t *testing.T) {
	rules, err := loadStubRules(writeRules(t, `[
		{"url": "**/a", "fulfill": {"json": 1, "headers": {"content-type": "text/explicit"}}},
		{"url": "**/b", "fulfill": {"json": 1, "contentType": "text/from-field"}},
		{"url": "**/c", "fulfill": {"body": "x", "contentType": "text/from-field", "headers": {"Content-Type": "text/explicit"}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"text/explicit", "text/from-field", "text/explicit"} {
		if got := headerEntry(rules[i].fulfill.ResponseHeaders, "Content-Type"); got != want {
			t.Errorf("rule %d: got Content-Type %q, want %q", i+1, got, want)
		}
		n := 0
		for _, h := range rules[i].fulfill.ResponseHeaders {
			if strings.EqualFold(h.Name, "Content-Type") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("rule %d: %d Content-Type headers, want 1", i+1, n)
		}
	}
}

func TestStubPreflightPayload(t *testing.T) {
	e := pauseEvent("OPTIONS", "https://h/api", map[string]string{
		"Origin":                         "https://o",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "x-spike,content-type",
	})
	p := stubPreflightPayload(e)
	if p.ResponseCode != 204 {
		t.Errorf("got status %d, want 204", p.ResponseCode)
	}
	for name, want := range map[string]string{
		"Access-Control-Allow-Origin":  "https://o",
		"Access-Control-Allow-Methods": "POST",
		"Access-Control-Allow-Headers": "x-spike,content-type",
	} {
		if got := headerEntry(p.ResponseHeaders, name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	if got := headerEntry(p.ResponseHeaders, "Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials: got %q, want true", got)
	}

	// Without an Origin to reflect the 204 allows any; a preflight that asks
	// for no headers gets no Allow-Headers at all rather than an empty one.
	p = stubPreflightPayload(pauseEvent("OPTIONS", "https://h/api",
		map[string]string{"Access-Control-Request-Method": "POST"}))
	if got := headerEntry(p.ResponseHeaders, "Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("originless preflight ACAO: got %q, want *", got)
	}
	for _, h := range p.ResponseHeaders {
		if strings.EqualFold(h.Name, "Access-Control-Allow-Headers") {
			t.Errorf("unexpected Access-Control-Allow-Headers: %q", h.Value)
		}
	}
}

// --- arguments and the open-page warning (no browser) ---

func TestParseStubArgs(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		path    string
		verbose bool
	}{
		{"just a file", []string{"rules.json"}, "rules.json", false},
		{"flag first", []string{"--verbose", "rules.json"}, "rules.json", true},
		{"flag last", []string{"rules.json", "-v"}, "rules.json", true},
		{"single dash spelling", []string{"-verbose", "rules.json"}, "rules.json", true},
		{"terminator", []string{"--", "-rules.json"}, "-rules.json", false},
		{"terminator after a flag", []string{"-v", "--", "--weird.json"}, "--weird.json", true},
	} {
		path, verbose, err := parseStubArgs(c.args)
		if err != nil || path != c.path || verbose != c.verbose {
			t.Errorf("%s: got %q/%v/%v, want %q/%v/nil", c.name, path, verbose, err, c.path, c.verbose)
		}
	}

	for _, c := range []struct {
		name string
		args []string
	}{
		{"no file", nil},
		{"only a flag", []string{"-v"}},
		{"two files", []string{"a.json", "b.json"}},
		{"unknown flag", []string{"--quiet", "rules.json"}},
		{"dash-leading file without --", []string{"-rules.json"}},
		{"inline value on a bool flag", []string{"--verbose=false", "rules.json"}},
	} {
		if path, _, err := parseStubArgs(c.args); err == nil {
			t.Errorf("%s: expected an error, got path %q", c.name, path)
		}
	}
}

func TestStubOpenPages(t *testing.T) {
	targets := []*proto.TargetTargetInfo{
		{Type: proto.TargetTargetInfoTypePage, URL: "https://example.com/"},
		{Type: proto.TargetTargetInfoTypePage, URL: "http://127.0.0.1:8080/app"},
		// None of these carries app traffic worth warning about.
		{Type: proto.TargetTargetInfoTypePage, URL: "about:blank"},
		{Type: proto.TargetTargetInfoTypePage, URL: "chrome://newtab/"},
		{Type: proto.TargetTargetInfoTypePage, URL: "chrome-extension://abc/options.html"},
		// Workers are covered by interception whenever they started.
		{Type: proto.TargetTargetInfoTypeServiceWorker, URL: "chrome-extension://abc/sw.js"},
		{Type: proto.TargetTargetInfoTypeBrowser, URL: ""},
	}
	if got := stubOpenPages(targets); got != 2 {
		t.Errorf("got %d open pages, want 2", got)
	}
	if got := stubOpenPages(nil); got != 0 {
		t.Errorf("got %d open pages for no targets, want 0", got)
	}
}

// --- end to end against a real browser ---

// host_permissions is what the *service worker's* fetch needs: without it that
// fetch carries an Origin of chrome-extension://<id>, the test server sends no
// CORS headers and it fails "Failed to fetch" before interception can matter
// (verified). The content script's fetch is same-origin with the page and needs
// none — though content_scripts matches grant the same origins in any case.
const stubSWManifest = `{
  "manifest_version": 3,
  "name": "Stub SW Extension",
  "version": "1.0.0",
  "background": {"service_worker": "sw.js"},
  "permissions": ["storage"],
  "host_permissions": ["<all_urls>"],
  "content_scripts": [{"matches": ["<all_urls>"], "js": ["cs.js"]}]
}`

// The content script proves the docs' claim that content-script fetches are
// intercepted too: it writes what it got into a place the test can read. The
// message it sends triggers the worker's ORGANIC fetch below.
const stubContentScript = `fetch("/api/user").then(r => r.text())
  .then(t => { document.title = "cs:" + t; })
  .catch(e => { document.title = "cs-ERR:" + e.message; });
chrome.runtime.sendMessage({url: location.origin + "/api/user"});`

// The worker fetches as extension logic reacting to a message — no debugger
// eval involved. That is the production shape the per-worker Fetch.enable
// exists for: without it this fetch never pauses and hits the live network.
const stubWorkerScript = `console.log("stub sw up");
chrome.runtime.onMessage.addListener((msg, sender, respond) => {
  fetch(msg.url).then(r => r.json())
    .then(data => chrome.storage.local.set({organic: data}))
    .catch(e => chrome.storage.local.set({organic: "ERR:" + e.message}));
  respond("ok");
  return true;
});`

// lockedBuffer lets the test read decision lines while answer goroutines may
// still be writing them.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// pollUntil calls cond every 100ms and reports whether it became true before
// the timeout. Interception starts and stops asynchronously, so what the page
// sees only settles a beat after the stub does.
func pollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestStub_EndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>stub test</body></html>")
	})
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"real":true}`)
	})
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "FINAL")
	})
	srvA := httptest.NewServer(mux)
	defer srvA.Close()
	// A second origin that sets no CORS headers: the page can only read its
	// responses if the stub answers both the preflight and the request.
	muxB := http.NewServeMux()
	muxB.HandleFunc("/cors", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "REAL-B")
	})
	srvB := httptest.NewServer(muxB)
	defer srvB.Close()

	extDir := filepath.Join(t.TempDir(), "stubext")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"manifest.json": stubSWManifest,
		"sw.js":         stubWorkerScript,
		"cs.js":         stubContentScript,
	} {
		if err := os.WriteFile(filepath.Join(extDir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	browser, _ := launchWithExtensions(t, extDir)

	rules, err := loadStubRules(writeRules(t, `[
		{"url": "**/api/user", "fulfill": {"json": {"stub": true}}},
		{"url": "**/telemetry/**", "abort": "internetdisconnected"},
		{"url": "**/cors", "fulfill": {"body": "STUB-B", "contentType": "text/plain"}},
		{"url": "**/final", "fulfill": {"body": "STUBBED-FINAL"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() { done <- runStub(ctx, browser, rules, out, false) }()

	page := browser.MustPage(srvA.URL + "/page")
	defer page.MustClose()
	// The deadline is per eval: an absolute page.Timeout would expire partway
	// through this long test and then panic the deferred MustClose.
	fetch := func(js string, args ...interface{}) string {
		t.Helper()
		res, err := page.Timeout(10*time.Second).Eval(js, args...)
		if err != nil {
			t.Fatalf("eval failed: %v", err)
		}
		return res.Value.Str()
	}
	fetchText := `url => fetch(url).then(r => r.text()).catch(e => "ERR:" + e.message)`

	// The poll works because this page is created after the concurrent enable:
	// interception only covers documents committed after it, so a page that
	// won that race would never be answered at all, not merely answered late.
	if !pollUntil(10*time.Second, func() bool {
		return fetch(fetchText, srvA.URL+"/api/user") == `{"stub":true}`
	}) {
		t.Fatal("the stub never started answering")
	}

	if got := fetch(fetchText, srvA.URL+"/telemetry/x"); got != "ERR:Failed to fetch" {
		t.Errorf("abort: got %q, want ERR:Failed to fetch", got)
	}
	// Unmatched requests reach the real server, and a redirect hop is continued
	// without consulting the rules: rule 4 does fulfill a direct /final, yet
	// the hop the 302 produces still lands on the real one.
	if got := fetch(fetchText, srvA.URL+"/final"); got != "STUBBED-FINAL" {
		t.Errorf("direct /final: got %q, want STUBBED-FINAL", got)
	}
	if got := fetch(fetchText, srvA.URL+"/redir"); got != "FINAL" {
		t.Errorf("redirect chain: got %q, want FINAL (the hop must bypass rule 4)", got)
	}
	// The preflight (custom header, cross-origin) and the request itself are
	// both answered by the stub; the real server never sends CORS headers.
	if got := fetch(`url => fetch(url, {headers: {"X-Stub-Test": "1"}}).then(r => r.text()).catch(e => "ERR:" + e.message)`,
		srvB.URL+"/cors"); got != "STUB-B" {
		t.Errorf("preflighted cross-origin fetch: got %q, want STUB-B; decisions:\n%s", got, out.String())
	}

	// And the extension's content script: reloading runs it with the stub
	// already answering, and it writes what its fetch returned into the title.
	if err := page.Timeout(15 * time.Second).Navigate(srvA.URL + "/page"); err != nil {
		t.Fatalf("reload for the content script: %v", err)
	}
	if err := page.Timeout(15 * time.Second).WaitLoad(); err != nil {
		t.Fatalf("reload for the content script: %v", err)
	}
	var title string
	if !pollUntil(10*time.Second, func() bool {
		title = fetch(`() => document.title`)
		return title == `cs:{"stub":true}`
	}) {
		t.Fatalf("content script fetch: title %q, want cs:{\"stub\":true}; decisions:\n%s", title, out.String())
	}
	// The same interception covers the extension's service worker.
	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}

	// The ORGANIC fetch — the content script's message made the worker fetch
	// as plain extension logic. Only the per-worker Fetch.enable intercepts
	// it; the browser-level enable alone never pauses this one. The reload
	// above re-sent the message with the stub live, so a stubbed value must
	// land even if the initial page load's message raced the worker enable.
	var organic string
	if !pollUntil(10*time.Second, func() bool {
		v, err := evalInServiceWorker(browser, sw,
			`chrome.storage.local.get("organic").then(r => JSON.stringify(r.organic))`)
		if err != nil {
			return false
		}
		organic = v.Str()
		return organic == `{"stub":true}`
	}) {
		t.Fatalf("organic sw fetch: got %q, want the stubbed body; decisions:\n%s", organic, out.String())
	}

	// With the worker's organic traffic settled, the eval path is exercised
	// with no in-flight worker pause to race the attach.

	// A sanity check that doubles as a stabilizer: attaching to a worker while
	// its own intercepted traffic is in flight can wedge the next eval fetch
	// (see CLAUDE.md), which is why the organic assertion above runs first —
	// this extra attach/detach cycle then costs nothing and asserts evals work.
	if v, err := evalInServiceWorker(browser, sw, "1 + 1"); err != nil || v.Int() != 2 {
		after, lerr := listServiceWorkers(browser, "")
		t.Fatalf("sw eval sanity: v=%v err=%v; workers now: %v (%v); decisions:\n%s", v, err, after, lerr, out.String())
	}
	swBody, err := evalInServiceWorker(browser, sw,
		fmt.Sprintf(`fetch(%q).then(r => r.text())`, srvA.URL+"/api/user"))
	if err != nil {
		pageAfter := fetch(fetchText, srvA.URL+"/api/user")
		t.Fatalf("sw fetch: %v; page interception after: %q; decisions:\n%s", err, pageAfter, out.String())
	}
	if swBody.Str() != `{"stub":true}` {
		t.Errorf("sw fetch: got %q, want the stubbed body", swBody.Str())
	}

	// Stopping the stub releases interception: requests reach the real server.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runStub: %v", err)
	}
	if !pollUntil(10*time.Second, func() bool {
		return fetch(fetchText, srvA.URL+"/api/user") == `{"real":true}`
	}) {
		t.Fatal("requests still stubbed after stop")
	}

	for _, want := range []string{
		"→ fulfill 200 (rule 1)",
		"→ abort internetdisconnected (rule 2)",
		"→ preflight 204 (rule 3)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("decision log lacks %q:\n%s", want, out.String())
		}
	}
}
