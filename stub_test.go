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
	if got := headerValue(rules[0].fulfill.ResponseHeaders, "Content-Type"); got != "application/json" {
		t.Errorf("rule 1: json implies application/json, got %q", got)
	}
	if rules[1].method != "GET" {
		t.Errorf("rule 2: method not normalized: %q", rules[1].method)
	}
	if rules[1].fulfill.ResponseCode != 500 {
		t.Errorf("rule 2: status not applied: %d", rules[1].fulfill.ResponseCode)
	}
	if got := headerValue(rules[1].fulfill.ResponseHeaders, "Content-Type"); got != "text/plain" {
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
	}
	for name, body := range bad {
		if _, err := loadStubRules(writeRules(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
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
		{"url": "**/passthrough", "continue": true}
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
		{"plain OPTIONS is not a preflight", pauseEvent("OPTIONS", "https://h/api/other", nil), stubFulfill, 1},
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
	// A cross-origin request gets ACAO reflected so the stubbed response is
	// readable; a same-origin one (no Origin header) gets no CORS headers.
	p := stubFulfillPayload(rules[0], pauseEvent("GET", "https://h/x", map[string]string{"Origin": "https://o"}))
	if got := headerValue(p.ResponseHeaders, "Access-Control-Allow-Origin"); got != "https://o" {
		t.Errorf("ACAO not reflected: %q", got)
	}
	p = stubFulfillPayload(rules[0], pauseEvent("GET", "https://h/x", nil))
	if got := headerValue(p.ResponseHeaders, "Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unexpected ACAO on same-origin fulfill: %q", got)
	}

	// A rule that sets ACAO itself is not second-guessed.
	rules, err = loadStubRules(writeRules(t, `[{"url": "**", "fulfill": {"headers": {"Access-Control-Allow-Origin": "*"}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	p = stubFulfillPayload(rules[0], pauseEvent("GET", "https://h/x", map[string]string{"Origin": "https://o"}))
	if got := headerValue(p.ResponseHeaders, "Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("rule's own ACAO overridden: %q", got)
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
		if got := headerValue(p.ResponseHeaders, name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

// headerValue reads one header from a fulfill payload's entries.
func headerValue(headers []*proto.FetchHeaderEntry, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// --- end to end against a real browser ---

// The SW needs host permissions or its cross-origin fetches are CORS-blocked
// before interception can matter.
const stubSWManifest = `{
  "manifest_version": 3,
  "name": "Stub SW Extension",
  "version": "1.0.0",
  "background": {"service_worker": "sw.js"},
  "host_permissions": ["<all_urls>"]
}`

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
		"sw.js":         `console.log("stub sw up");`,
	} {
		if err := os.WriteFile(filepath.Join(extDir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	browser, _ := launchWithExtensions(t, extDir)

	rules, err := loadStubRules(writeRules(t, `[
		{"url": "**/api/user", "fulfill": {"json": {"stub": true}}},
		{"url": "**/telemetry/**", "abort": "internetdisconnected"},
		{"url": "**/cors", "fulfill": {"body": "STUB-B", "contentType": "text/plain"}}
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

	// The enable races the first request; poll until the stub answers.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if fetch(fetchText, srvA.URL+"/api/user") == `{"stub":true}` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stub never started answering")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if got := fetch(fetchText, srvA.URL+"/telemetry/x"); got != "ERR:Failed to fetch" {
		t.Errorf("abort: got %q, want ERR:Failed to fetch", got)
	}
	// Unmatched requests reach the real server, redirect hops included.
	if got := fetch(fetchText, srvA.URL+"/redir"); got != "FINAL" {
		t.Errorf("redirect chain: got %q, want FINAL", got)
	}
	// The preflight (custom header, cross-origin) and the request itself are
	// both answered by the stub; the real server never sends CORS headers.
	if got := fetch(`url => fetch(url, {headers: {"X-Stub-Test": "1"}}).then(r => r.text()).catch(e => "ERR:" + e.message)`,
		srvB.URL+"/cors"); got != "STUB-B" {
		t.Errorf("preflighted cross-origin fetch: got %q, want STUB-B; decisions:\n%s", got, out.String())
	}

	// The same interception covers the extension's service worker.
	sw, err := waitServiceWorker(browser, "", 15*time.Second)
	if err != nil {
		t.Fatalf("no service worker: %v", err)
	}
	// This is a sanity check AND a required warm-up: a fetch launched by the
	// FIRST eval in a freshly-attached worker session races browser-level
	// interception inside Chromium and can wedge with its pause never
	// surfacing (1 in 10 without this; 13/13 with). The attach/detach cycle
	// this eval performs leaves the worker stable for the fetch below.
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
	deadline = time.Now().Add(10 * time.Second)
	for {
		if fetch(fetchText, srvA.URL+"/api/user") == `{"real":true}` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("requests still stubbed after stop")
		}
		time.Sleep(100 * time.Millisecond)
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
