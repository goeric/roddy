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
	// A pointer distinguishes an absent status (200) from an explicit 0, which
	// is not a status at all and must fail validation.
	Status      *int              `json:"status"`
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
	// Decode stops at the end of the first value, so a second array (or any
	// other trailing text) would otherwise load as nothing but the first.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid rules file %s: trailing content after the rules array", path)
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
	if f.Status != nil {
		if *f.Status < 100 || *f.Status > 599 {
			return nil, fmt.Errorf("invalid fulfill status %d", *f.Status)
		}
		p.ResponseCode = *f.Status
	}

	sources := 0
	if f.Body != nil {
		sources++
	}
	if f.JSON != nil {
		sources++
	}
	if f.Path != "" {
		sources++
	}
	// Counted before any of them is read, so a rule naming two sources reports
	// the conflict rather than whichever one happens to fail first.
	if sources > 1 {
		return nil, fmt.Errorf(`fulfill takes at most one of "body", "json", "path"`)
	}

	contentType := f.ContentType
	switch {
	case f.Body != nil:
		p.Body = []byte(*f.Body)
	case f.JSON != nil:
		var compact bytes.Buffer
		if err := json.Compact(&compact, f.JSON); err != nil {
			return nil, fmt.Errorf(`invalid fulfill "json": %v`, err)
		}
		p.Body = compact.Bytes()
		if contentType == "" {
			contentType = "application/json"
		}
	case f.Path != "":
		// A relative fixture resolves against the rules file; all of them load
		// up front, so the holder does no disk I/O with a request paused on it.
		fixture := f.Path
		if !filepath.IsAbs(fixture) {
			fixture = filepath.Join(fixtureDir, fixture)
		}
		body, err := os.ReadFile(fixture)
		if err != nil {
			return nil, fmt.Errorf("cannot read fulfill path: %v", err)
		}
		p.Body = body
	}
	// rod marshals a nil Body as JSON null, which Chrome rejects with "binary
	// value expected" — an empty body must still be a (zero-length) binary.
	if p.Body == nil {
		p.Body = []byte{}
	}

	// Chrome rejects a malformed header per request ("Invalid header") and the
	// request then falls through to the LIVE network, so these fail at load
	// like everything else rather than at the first request that matches.
	for _, name := range sortedKeys(f.Headers) {
		value := f.Headers[name]
		if !validHeaderName(name) {
			return nil, fmt.Errorf("invalid fulfill header name %q", name)
		}
		if !validHeaderValue(value) {
			return nil, fmt.Errorf("invalid fulfill header value for %q: control characters are not allowed", name)
		}
		p.ResponseHeaders = append(p.ResponseHeaders, &proto.FetchHeaderEntry{Name: name, Value: value})
	}
	if contentType != "" && headerEntry(p.ResponseHeaders, "Content-Type") == "" {
		if !validHeaderValue(contentType) {
			return nil, fmt.Errorf(`invalid fulfill "contentType" %q: control characters are not allowed`, contentType)
		}
		p.ResponseHeaders = append(p.ResponseHeaders, &proto.FetchHeaderEntry{Name: "Content-Type", Value: contentType})
	}
	return p, nil
}

// validHeaderName reports whether s is an RFC 7230 token: no controls, no
// space, none of the separators.
func validHeaderName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`"(),/:;<=>?@[\]{}`, c) >= 0 {
			return false
		}
	}
	return s != ""
}

// validHeaderValue rejects the control characters a header value may not
// carry, CR and LF among them.
func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
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
	// fetch() normalizes only the six common verbs, so an unusual one arrives
	// exactly as the caller typed it ("patch"); rule methods are uppercased.
	method := strings.ToUpper(e.Request.Method)
	if method == "OPTIONS" {
		if acrm := requestHeader(e.Request.Headers, "Access-Control-Request-Method"); acrm != "" {
			if i := matchStubRule(rules, strings.ToUpper(acrm), e.Request.URL); i >= 0 && (rules[i].fulfill != nil || rules[i].abort != "") {
				return stubDecision{stubPreflight, i}
			}
			return stubDecision{stubContinue, -1}
		}
	}
	i := matchStubRule(rules, method, e.Request.URL)
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
// A request bearing an Origin header gets Access-Control-Allow-Origin
// reflected (as Playwright does, which additionally checks the origins differ
// — reflecting regardless is harmless) so the stubbed response is readable by
// the page, unless the rule set its own.
//
// The template is never mutated: this copies the struct and the header slice,
// and the entries and Body it then shares stay read-only for their lifetime.
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
// every answer runs in its own goroutine: rod runs the callbacks synchronously
// inside wait, so answering inline would serialize every pause behind the
// previous answer's round-trip.
func runStub(ctx context.Context, browser *rod.Browser, rules []stubRule, out io.Writer, verbose bool) error {
	var mu sync.Mutex
	logf := func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(out, format+"\n", args...)
	}

	// A browser-level EachEvent delivers every session's events (rod's filter is
	// `sessionID == "" || msg.SessionID == sessionID`, and Browser.EachEvent
	// passes ""), so the sid parameter is not what makes them arrive: it says
	// which session to answer on. Every pause observed on this Chromium carries
	// sid "" — even with flat sessions attached to service workers, as every
	// sw/storage command does — so the routing is defensive.
	wait := browser.Context(ctx).EachEvent(func(e *proto.FetchRequestPaused, sid proto.TargetSessionID) {
		// Answers go out on a deadlined client: a wedged browser must fail the
		// answer rather than park its goroutine forever.
		var client proto.Client = browser.Timeout(defaultTimeout)
		if sid != "" {
			client = browser.Timeout(defaultTimeout).PageFromSession(sid)
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
		// Reached only when the caller cancels (the tests do; Ctrl+C kills the
		// process instead, and Chrome's own disconnect cleanup then releases
		// interception and auto-continues the in-flight pauses). Release it
		// explicitly so a canceled-but-still-connected browser flows again.
		_ = proto.FetchDisable{}.Call(browser.Timeout(defaultTimeout))
		return nil
	}
	return fmt.Errorf("browser connection lost")
}

// answerPause resolves one paused request. Every pause must be answered or the
// request hangs forever, so a failed answer is logged unconditionally — with
// what the fallback continue then did, since a request nobody answered is
// otherwise invisible.
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
		// else releases it, so fall back to continuing it. The fallback failing
		// too means the request is already gone (canceled, page closed).
		if ferr := (proto.FetchContinueRequest{RequestID: e.RequestID}).Call(client); ferr != nil {
			logf("%s %s → answer failed: %v; request already gone (%v)", e.Request.Method, e.Request.URL, err, ferr)
		} else {
			logf("%s %s → answer failed: %v; released to the real network", e.Request.Method, e.Request.URL, err)
		}
	}
}

// parseStubArgs pulls --verbose out of args wherever it appears, the way
// parseStorageFlags does, and returns the single positional rules file. "--"
// ends the flags for a file name that really does start with a dash.
func parseStubArgs(args []string) (path string, verbose bool, err error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		name, _, inline := strings.Cut(args[i], "=")
		switch name {
		case "--verbose", "-verbose", "-v":
			if inline {
				return "", false, fmt.Errorf("flag %s does not take a value", name)
			}
			verbose = true
		default:
			if storageFlagLike(name) {
				return "", false, fmt.Errorf(`unknown flag: %s (use "--" before a file name that starts with a dash)`, name)
			}
			rest = append(rest, args[i])
		}
	}
	switch {
	case len(rest) == 0:
		return "", false, fmt.Errorf("missing rules file")
	case len(rest) > 1:
		return "", false, fmt.Errorf("stub takes exactly one rules file")
	}
	return rest[0], verbose, nil
}

// stubOpenPages counts the pages the stub is about to miss: interception
// applies per committed document, so a page open before it starts keeps the
// live network until it is reloaded. about:blank and Chrome's own pages carry
// no traffic worth warning about.
func stubOpenPages(targets []*proto.TargetTargetInfo) int {
	n := 0
	for _, t := range targets {
		if t.Type != proto.TargetTargetInfoTypePage || t.URL == "about:blank" {
			continue
		}
		if strings.HasPrefix(t.URL, "chrome://") || strings.HasPrefix(t.URL, "chrome-extension://") {
			continue
		}
		n++
	}
	return n
}

// cmdStub handles "roddy stub <rules-file>": browser-wide request stubbing
// held in the foreground, logs --follow style, until interrupted.
func cmdStub(args []string) {
	path, verbose, err := parseStubArgs(args)
	if err != nil {
		fatal("%v\n%s", err, stubUsage)
	}

	rules, err := loadStubRules(path)
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

	// Interception applies per committed document, so pages that are already
	// open are not covered until they navigate again. Warning beats reloading
	// them behind the user's back.
	if list, lerr := (proto.TargetGetTargets{}).Call(browser.Timeout(defaultTimeout)); lerr != nil {
		fmt.Fprintf(os.Stderr, "note: cannot check for open pages: %v\n", lerr)
	} else if n := stubOpenPages(list.TargetInfos); n > 0 {
		fmt.Fprintf(os.Stderr, "note: %d open page(s) keep the live network until reloaded — start the stub first, or roddy reload\n", n)
	}

	fmt.Printf("stub: %d rules active (Ctrl+C to stop)\n", len(rules))
	if err := runStub(context.Background(), browser, rules, os.Stdout, verbose); err != nil {
		// A stub only ends by signal; ending on its own is a failure.
		fatal("%v", err)
	}
}
