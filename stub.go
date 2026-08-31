package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const stubUsage = "usage: roddy stub <rules-file> [--verbose | -v]"

// compileGlob translates a Playwright-dialect URL glob to a regexp: '*' stays
// inside a path segment, '**' crosses them ('/**/' spans zero or more whole
// segments), '{a,b}' alternates without nesting, '?' is a literal, backslash
// escapes. Ported from Playwright's globToRegexPattern (urlMatch.ts) so
// patterns copy verbatim from Playwright tests.
func compileGlob(glob string) (*regexp.Regexp, error) {
	isSpecial := func(c byte) bool {
		return strings.IndexByte(`$^+.*()|\?{}[]`, c) >= 0
	}
	var b strings.Builder
	b.WriteByte('^')
	inGroup := false
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		if c == '\\' && i+1 < len(glob) {
			i++
			if isSpecial(glob[i]) {
				b.WriteByte('\\')
			}
			b.WriteByte(glob[i])
			continue
		}
		if c == '*' {
			var before byte
			if i > 0 {
				before = glob[i-1]
			}
			stars := 1
			for i+1 < len(glob) && glob[i+1] == '*' {
				stars++
				i++
			}
			switch {
			case stars == 1:
				b.WriteString(`([^/]*)`)
			case i+1 < len(glob) && glob[i+1] == '/':
				if before == '/' {
					b.WriteString(`((.+/)|)`)
				} else {
					b.WriteString(`(.*/)`)
				}
				i++
			default:
				b.WriteString(`(.*)`)
			}
			continue
		}
		switch c {
		case '{':
			if inGroup {
				return nil, fmt.Errorf("invalid glob pattern %q: nested '{' is not supported", glob)
			}
			inGroup = true
			b.WriteByte('(')
		case '}':
			if !inGroup {
				return nil, fmt.Errorf("invalid glob pattern %q: unmatched '}'", glob)
			}
			inGroup = false
			b.WriteByte(')')
		case ',':
			if inGroup {
				b.WriteByte('|')
			} else {
				b.WriteString(`\,`)
			}
		default:
			if isSpecial(c) {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
	}
	if inGroup {
		return nil, fmt.Errorf("invalid glob pattern %q: unmatched '{'", glob)
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}

// stubAbortReasons are the abort codes a rule may name — Playwright's set,
// which is the CDP error-reason enum verbatim.
var stubAbortReasons = map[string]proto.NetworkErrorReason{
	"aborted":              proto.NetworkErrorReasonAborted,
	"accessdenied":         proto.NetworkErrorReasonAccessDenied,
	"addressunreachable":   proto.NetworkErrorReasonAddressUnreachable,
	"blockedbyclient":      proto.NetworkErrorReasonBlockedByClient,
	"blockedbyresponse":    proto.NetworkErrorReasonBlockedByResponse,
	"connectionaborted":    proto.NetworkErrorReasonConnectionAborted,
	"connectionclosed":     proto.NetworkErrorReasonConnectionClosed,
	"connectionfailed":     proto.NetworkErrorReasonConnectionFailed,
	"connectionrefused":    proto.NetworkErrorReasonConnectionRefused,
	"connectionreset":      proto.NetworkErrorReasonConnectionReset,
	"internetdisconnected": proto.NetworkErrorReasonInternetDisconnected,
	"namenotresolved":      proto.NetworkErrorReasonNameNotResolved,
	"timedout":             proto.NetworkErrorReasonTimedOut,
	"failed":               proto.NetworkErrorReasonFailed,
}

// stubRule is one compiled rule: a matcher plus exactly one verb. A rule with
// neither fulfill nor abort is an explicit continue.
type stubRule struct {
	pattern string // the glob as written, for log lines
	url     *regexp.Regexp
	method  string                     // uppercased; empty matches any
	fulfill *proto.FetchFulfillRequest // template; RequestID is filled per pause
	abort   proto.NetworkErrorReason
}

// The wire format mirrors Playwright's route verbs (fulfill/abort/continue)
// and fulfill fields (status/headers/contentType/body/json/path), so a rule
// reads like a serialized route.fulfill() call.
type stubRuleJSON struct {
	URL      string           `json:"url"`
	Method   string           `json:"method"`
	Fulfill  *stubFulfillJSON `json:"fulfill"`
	Abort    json.RawMessage  `json:"abort"`
	Continue json.RawMessage  `json:"continue"`
}

type stubFulfillJSON struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers"`
	ContentType string            `json:"contentType"`
	Body        *string           `json:"body"`
	JSON        json.RawMessage   `json:"json"`
	Path        string            `json:"path"`
}

// loadStubRules parses, validates and compiles a rules file. Everything that
// can fail — globs, abort reasons, body fixtures — fails here, before any
// browser is touched: the holder must never discover a broken rule while a
// request sits paused on it.
func loadStubRules(path string) ([]stubRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read rules file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw []stubRuleJSON
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid rules file %s: %v", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("rules file %s has no rules", path)
	}

	rules := make([]stubRule, 0, len(raw))
	for i, r := range raw {
		rule, err := compileStubRule(r, filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("rule %d: %v", i+1, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func compileStubRule(r stubRuleJSON, fixtureDir string) (stubRule, error) {
	if r.URL == "" {
		return stubRule{}, fmt.Errorf(`missing "url"`)
	}
	re, err := compileGlob(r.URL)
	if err != nil {
		return stubRule{}, err
	}
	rule := stubRule{pattern: r.URL, url: re, method: strings.ToUpper(r.Method)}

	verbs := 0
	if r.Fulfill != nil {
		verbs++
	}
	if r.Abort != nil {
		verbs++
	}
	if r.Continue != nil {
		verbs++
	}
	if verbs != 1 {
		return stubRule{}, fmt.Errorf(`need exactly one of "fulfill", "abort", "continue"`)
	}

	switch {
	case r.Continue != nil:
		if string(r.Continue) != "true" {
			return stubRule{}, fmt.Errorf(`"continue" only takes true`)
		}
	case r.Abort != nil:
		if string(r.Abort) == "true" {
			rule.abort = proto.NetworkErrorReasonFailed
			break
		}
		var name string
		if err := json.Unmarshal(r.Abort, &name); err != nil {
			return stubRule{}, fmt.Errorf(`"abort" takes true or an error code`)
		}
		reason, ok := stubAbortReasons[name]
		if !ok {
			return stubRule{}, fmt.Errorf("unknown abort code %q (codes: %s)", name, strings.Join(sortedKeys(stubAbortReasons), ", "))
		}
		rule.abort = reason
	default:
		f, err := compileStubFulfill(r.Fulfill, fixtureDir)
		if err != nil {
			return stubRule{}, err
		}
		rule.fulfill = f
	}
	return rule, nil
}

func compileStubFulfill(f *stubFulfillJSON, fixtureDir string) (*proto.FetchFulfillRequest, error) {
	p := &proto.FetchFulfillRequest{ResponseCode: 200}
	if f.Status != 0 {
		if f.Status < 100 || f.Status > 599 {
			return nil, fmt.Errorf("invalid fulfill status %d", f.Status)
		}
		p.ResponseCode = f.Status
	}

	sources := 0
	contentType := f.ContentType
	if f.Body != nil {
		sources++
		p.Body = []byte(*f.Body)
	}
	if f.JSON != nil {
		sources++
		var compact bytes.Buffer
		if err := json.Compact(&compact, f.JSON); err != nil {
			return nil, fmt.Errorf(`invalid fulfill "json": %v`, err)
		}
		p.Body = compact.Bytes()
		if contentType == "" {
			contentType = "application/json"
		}
	}
	if f.Path != "" {
		sources++
		// Fixtures resolve relative to the rules file and load up front, so the
		// holder does no disk I/O with a request paused on it.
		body, err := os.ReadFile(filepath.Join(fixtureDir, f.Path))
		if err != nil {
			return nil, fmt.Errorf("cannot read fulfill path: %v", err)
		}
		p.Body = body
	}
	if sources > 1 {
		return nil, fmt.Errorf(`fulfill takes at most one of "body", "json", "path"`)
	}
	// rod marshals a nil Body as JSON null, which Chrome rejects with "binary
	// value expected" — an empty body must still be a (zero-length) binary.
	if p.Body == nil {
		p.Body = []byte{}
	}

	for _, name := range sortedKeys(f.Headers) {
		p.ResponseHeaders = append(p.ResponseHeaders, &proto.FetchHeaderEntry{Name: name, Value: f.Headers[name]})
	}
	if contentType != "" && headerEntry(p.ResponseHeaders, "Content-Type") == "" {
		p.ResponseHeaders = append(p.ResponseHeaders, &proto.FetchHeaderEntry{Name: "Content-Type", Value: contentType})
	}
	return p, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func headerEntry(headers []*proto.FetchHeaderEntry, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// requestHeader reads one request header case-insensitively.
func requestHeader(h proto.NetworkHeaders, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v.Str()
		}
	}
	return ""
}

type stubKind int

const (
	stubContinue    stubKind = iota // unmatched, or an explicit continue rule
	stubContinueHop                 // redirect hop: never re-matched (Chromium re-pauses each hop)
	stubFulfill
	stubAbort
	stubPreflight // synthesized 204 for a preflight whose real request is stubbed
)

type stubDecision struct {
	kind stubKind
	rule int // index into rules, -1 when no rule matched
}

func matchStubRule(rules []stubRule, method, url string) int {
	for i, r := range rules {
		if r.method != "" && r.method != method {
			continue
		}
		if r.url.MatchString(url) {
			return i
		}
	}
	return -1
}

// stubDecide picks the action for one paused request. First matching rule
// wins, top of the file first. A CORS preflight (OPTIONS announcing a method)
// is answered with a 204 when the request it announces would be fulfilled or
// aborted — the real server may not exist — and left alone otherwise, so
// genuine CORS behavior stays testable under continue.
func stubDecide(rules []stubRule, e *proto.FetchRequestPaused) stubDecision {
	if e.RedirectedRequestID != "" {
		return stubDecision{stubContinueHop, -1}
	}
	if e.Request.Method == "OPTIONS" {
		if acrm := requestHeader(e.Request.Headers, "Access-Control-Request-Method"); acrm != "" {
			if i := matchStubRule(rules, strings.ToUpper(acrm), e.Request.URL); i >= 0 && (rules[i].fulfill != nil || rules[i].abort != "") {
				return stubDecision{stubPreflight, i}
			}
			return stubDecision{stubContinue, -1}
		}
	}
	i := matchStubRule(rules, e.Request.Method, e.Request.URL)
	switch {
	case i < 0:
		return stubDecision{stubContinue, -1}
	case rules[i].fulfill != nil:
		return stubDecision{stubFulfill, i}
	case rules[i].abort != "":
		return stubDecision{stubAbort, i}
	default:
		return stubDecision{stubContinue, i}
	}
}

// stubFulfillPayload instantiates a rule's fulfill template for one request.
// A cross-origin request gets Access-Control-Allow-Origin reflected (as
// Playwright does) so the stubbed response is readable by the page, unless
// the rule set its own.
func stubFulfillPayload(r stubRule, e *proto.FetchRequestPaused) *proto.FetchFulfillRequest {
	p := *r.fulfill
	p.RequestID = e.RequestID
	p.ResponseHeaders = append([]*proto.FetchHeaderEntry{}, r.fulfill.ResponseHeaders...)
	if origin := requestHeader(e.Request.Headers, "Origin"); origin != "" && headerEntry(p.ResponseHeaders, "Access-Control-Allow-Origin") == "" {
		p.ResponseHeaders = append(p.ResponseHeaders,
			&proto.FetchHeaderEntry{Name: "Access-Control-Allow-Origin", Value: origin},
			&proto.FetchHeaderEntry{Name: "Access-Control-Allow-Credentials", Value: "true"},
			&proto.FetchHeaderEntry{Name: "Vary", Value: "Origin"})
	}
	return &p
}

// stubPreflightPayload synthesizes the 204 that stands in for a stubbed
// endpoint's preflight, reflecting what the request asked to send.
func stubPreflightPayload(e *proto.FetchRequestPaused) *proto.FetchFulfillRequest {
	origin := requestHeader(e.Request.Headers, "Origin")
	if origin == "" {
		origin = "*"
	}
	headers := []*proto.FetchHeaderEntry{
		{Name: "Access-Control-Allow-Origin", Value: origin},
		{Name: "Access-Control-Allow-Credentials", Value: "true"},
		{Name: "Access-Control-Allow-Methods", Value: requestHeader(e.Request.Headers, "Access-Control-Request-Method")},
	}
	if acrh := requestHeader(e.Request.Headers, "Access-Control-Request-Headers"); acrh != "" {
		headers = append(headers, &proto.FetchHeaderEntry{Name: "Access-Control-Allow-Headers", Value: acrh})
	}
	// Body stays a non-nil empty slice: see compileStubFulfill.
	return &proto.FetchFulfillRequest{RequestID: e.RequestID, ResponseCode: 204, ResponseHeaders: headers, Body: []byte{}}
}

// runStub holds browser-wide interception until ctx ends or the connection
// drops, answering every pause: verified on this Chromium, one browser-level
// Fetch.enable covers pages, content scripts and MV3 service workers, and
// interception itself bypasses the HTTP cache.
//
// The subscription is registered before the enable (the logs.go lesson), and
// every answer runs in its own goroutine: rod delivers events synchronously
// inside wait, so answering inline would deadlock the CDP reader against the
// answer's own round-trip.
func runStub(ctx context.Context, browser *rod.Browser, rules []stubRule, out io.Writer, verbose bool) error {
	var mu sync.Mutex
	logf := func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(out, format+"\n", args...)
	}

	// The callback takes the session ID: without that parameter rod delivers
	// only the browser session's events, and a pause Chrome tags with another
	// session's ID — seen when a flat session is attached to a service worker,
	// as every sw/storage command does — would be dropped and hang its request.
	// The answer goes back on the session the pause arrived on.
	wait := browser.Context(ctx).EachEvent(func(e *proto.FetchRequestPaused, sid proto.TargetSessionID) {
		var client proto.Client = browser
		if sid != "" {
			client = browser.PageFromSession(sid)
		}
		go answerPause(client, rules, e, logf, verbose)
	})

	if err := (proto.FetchEnable{
		Patterns: []*proto.FetchRequestPattern{{URLPattern: "*", RequestStage: proto.FetchRequestStageRequest}},
	}).Call(browser.Timeout(defaultTimeout)); err != nil {
		return fmt.Errorf("failed to enable interception: %w", err)
	}

	wait()
	if ctx.Err() != nil {
		// A deliberate stop: release interception so later requests flow again
		// even though this CDP connection is about to close anyway.
		_ = proto.FetchDisable{}.Call(browser)
		return nil
	}
	return fmt.Errorf("browser connection lost")
}

// answerPause resolves one paused request. Every pause must be answered or
// the request hangs forever; answer errors are expected when a request is
// canceled or the page closes mid-flight, so they only print under --verbose.
func answerPause(client proto.Client, rules []stubRule, e *proto.FetchRequestPaused, logf func(string, ...interface{}), verbose bool) {
	d := stubDecide(rules, e)
	var err error
	switch d.kind {
	case stubFulfill:
		p := stubFulfillPayload(rules[d.rule], e)
		logf("%s %s → fulfill %d (rule %d)", e.Request.Method, e.Request.URL, p.ResponseCode, d.rule+1)
		err = p.Call(client)
	case stubAbort:
		logf("%s %s → abort %s (rule %d)", e.Request.Method, e.Request.URL, strings.ToLower(string(rules[d.rule].abort)), d.rule+1)
		err = proto.FetchFailRequest{RequestID: e.RequestID, ErrorReason: rules[d.rule].abort}.Call(client)
	case stubPreflight:
		logf("%s %s → preflight 204 (rule %d)", e.Request.Method, e.Request.URL, d.rule+1)
		err = stubPreflightPayload(e).Call(client)
	case stubContinueHop:
		if verbose {
			logf("%s %s → continue (redirect hop)", e.Request.Method, e.Request.URL)
		}
		err = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(client)
	default:
		if d.rule >= 0 {
			logf("%s %s → continue (rule %d)", e.Request.Method, e.Request.URL, d.rule+1)
		} else if verbose {
			logf("%s %s → continue", e.Request.Method, e.Request.URL)
		}
		err = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(client)
	}
	if err != nil {
		// A failed answer leaves the request paused forever unless something
		// else releases it, so fall back to continuing it; if the request is
		// already gone (canceled, page closed) the fallback fails quietly too.
		logf("%s %s → answer failed: %v", e.Request.Method, e.Request.URL, err)
		_ = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(client)
	}
}

// cmdStub handles "roddy stub <rules-file>": browser-wide request stubbing
// held in the foreground, logs --follow style, until interrupted.
func cmdStub(args []string) {
	verbose := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--verbose", "-v":
			verbose = true
		default:
			if storageFlagLike(a) {
				fatal("unknown flag: %s\n%s", a, stubUsage)
			}
			rest = append(rest, a)
		}
	}
	if len(rest) != 1 {
		fatal("%s", stubUsage)
	}

	rules, err := loadStubRules(rest[0])
	if err != nil {
		fatal("%v", err)
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	fmt.Printf("stub: %d rules active (Ctrl+C to stop)\n", len(rules))
	if err := runStub(context.Background(), browser, rules, os.Stdout, verbose); err != nil {
		// A stub only ends by signal; ending on its own is a failure.
		fatal("%v", err)
	}
}
