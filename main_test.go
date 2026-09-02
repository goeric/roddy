package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// testEnv holds a shared browser and test HTTP server for all tests.
type testEnv struct {
	browser *rod.Browser
	server  *httptest.Server
}

var env *testEnv

func TestMain(m *testing.M) {
	// Launch headless Chrome once for all tests
	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Headless(true).
		Leakless(false)

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()

	// Start test HTTP server with known HTML fixtures
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/form", handleForm)
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/download", handleDownload)
	mux.HandleFunc("/testfile.txt", handleTestFile)
	mux.HandleFunc("/empty", handleEmpty)
	mux.HandleFunc("/sw-page", handleSWPage)
	mux.HandleFunc("/page-sw.js", handlePageSW)
	mux.HandleFunc("/logs-page", handleLogsPage)
	// The "/" handler answers every unregistered path, so a 404 has to be one.
	mux.HandleFunc("/missing-resource", http.NotFound)
	server := httptest.NewServer(mux)

	env = &testEnv{browser: browser, server: server}

	code := m.Run()

	server.Close()
	browser.MustClose()
	os.Exit(code)
}

// --- HTML fixtures ---

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Test Page</title></head>
<body>
  <nav aria-label="Main">
    <a href="/about">About</a>
    <a href="/contact">Contact</a>
  </nav>
  <main>
    <h1>Welcome</h1>
    <p>Hello world</p>
    <button id="submit-btn">Submit</button>
    <button id="cancel-btn" disabled>Cancel</button>
  </main>
</body>
</html>`))
}

func handleForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Form Page</title></head>
<body>
  <h1>Contact Us</h1>
  <form>
    <label for="name-input">Name</label>
    <input id="name-input" type="text" aria-required="true">
    <label for="email-input">Email</label>
    <input id="email-input" type="email">
    <select id="topic" aria-label="Topic">
      <option value="general">General</option>
      <option value="support">Support</option>
    </select>
    <button type="submit">Send</button>
  </form>
</body>
</html>`))
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Upload Page</title></head>
<body>
  <input id="file-input" type="file" accept="image/*">
  <span id="file-name"></span>
  <script>
    document.getElementById('file-input').addEventListener('change', function(e) {
      document.getElementById('file-name').textContent = e.target.files[0] ? e.target.files[0].name : '';
    });
  </script>
</body>
</html>`))
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Download Page</title></head>
<body>
  <a id="file-link" href="/testfile.txt">Download file</a>
  <a id="data-link" href="data:text/plain;base64,SGVsbG8gV29ybGQ=">Download data</a>
  <img id="test-img" src="/testfile.txt">
</body>
</html>`))
}

func handleTestFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("Hello World"))
}

func handleEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Empty Page</title></head>
<body></body>
</html>`))
}

// handleSWPage serves a page that registers an ordinary (non-extension) service
// worker, so tests can check those are filtered out. The test server is on
// 127.0.0.1, which counts as a secure context, so registration is allowed.
func handleSWPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>SW Page</title></head>
<body>
  <script>navigator.serviceWorker.register("/page-sw.js");</script>
</body>
</html>`))
}

func handlePageSW(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript")
	w.Write([]byte(`self.addEventListener("install", () => self.skipWaiting());`))
}

// --- Helper: navigate to a fixture and return the page ---

func navigateTo(t *testing.T, path string) *rod.Page {
	t.Helper()
	page := env.browser.MustPage(env.server.URL + path)
	page.MustWaitLoad()
	t.Cleanup(func() { page.MustClose() })
	return page
}

// =====================
// ax-tree tests (RED)
// =====================

func TestAXTree_ReturnsNodes(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	// Sanity: we should get nodes back
	if len(result.Nodes) == 0 {
		t.Fatal("expected nodes in accessibility tree, got 0")
	}

	// Now test our formatting function
	out := formatAXTree(result.Nodes)
	if out == "" {
		t.Fatal("formatAXTree returned empty string")
	}
	if !strings.Contains(out, "Welcome") {
		t.Errorf("tree should contain heading text 'Welcome', got:\n%s", out)
	}
	if !strings.Contains(out, "button") {
		t.Errorf("tree should contain 'button' role, got:\n%s", out)
	}
	if !strings.Contains(out, "Submit") {
		t.Errorf("tree should contain button name 'Submit', got:\n%s", out)
	}
}

func TestAXTree_Indentation(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTree(result.Nodes)
	lines := strings.Split(out, "\n")

	// Root node should have no indentation
	if len(lines) == 0 {
		t.Fatal("no lines in output")
	}
	if strings.HasPrefix(lines[0], " ") {
		t.Errorf("root node should not be indented, got: %q", lines[0])
	}

	// Some lines should be indented (children)
	hasIndented := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") {
			hasIndented = true
			break
		}
	}
	if !hasIndented {
		t.Errorf("expected some indented lines for child nodes, got:\n%s", out)
	}
}

func TestAXTree_SkipsIgnoredNodes(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTree(result.Nodes)

	// Count ignored vs total
	ignoredCount := 0
	for _, node := range result.Nodes {
		if node.Ignored {
			ignoredCount++
		}
	}

	// If there are ignored nodes, they shouldn't appear in text output
	if ignoredCount > 0 {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= len(result.Nodes) {
			t.Errorf("text output should skip ignored nodes: %d lines for %d nodes (%d ignored)",
				len(lines), len(result.Nodes), ignoredCount)
		}
	}
}

func TestAXTree_DepthLimit(t *testing.T) {
	page := navigateTo(t, "/")
	full, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}

	depth := 2
	limited, err := proto.AccessibilityGetFullAXTree{Depth: &depth}.Call(page)
	if err != nil {
		t.Fatalf("CDP call with depth failed: %v", err)
	}

	if len(limited.Nodes) >= len(full.Nodes) {
		t.Errorf("depth-limited tree (%d nodes) should have fewer nodes than full tree (%d nodes)",
			len(limited.Nodes), len(full.Nodes))
	}
}

func TestAXTree_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTreeJSON(result.Nodes)
	// Must be valid JSON
	var parsed []interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\nOutput:\n%s", err, out[:min(len(out), 500)])
	}
	if len(parsed) == 0 {
		t.Error("JSON output should contain nodes")
	}
}

// =====================
// ax-find tests (RED)
// =====================

func TestAXFind_ByRole(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "", "button")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 buttons, got %d", len(nodes))
	}

	out := formatAXNodeList(nodes)
	if !strings.Contains(out, "Submit") {
		t.Errorf("output should contain 'Submit' button, got:\n%s", out)
	}
	if !strings.Contains(out, "Cancel") {
		t.Errorf("output should contain 'Cancel' button, got:\n%s", out)
	}
}

func TestAXFind_ByName(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "Submit", "")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least 1 node named 'Submit', got 0")
	}
	out := formatAXNodeList(nodes)
	if !strings.Contains(out, "Submit") {
		t.Errorf("output should contain 'Submit', got:\n%s", out)
	}
}

func TestAXFind_ByNameAndRoleExact(t *testing.T) {
	page := navigateTo(t, "/")
	// Combining name + role should give exactly one result
	nodes, err := queryAXNodes(page, "Submit", "button")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected exactly 1 button named 'Submit', got %d", len(nodes))
	}
}

func TestAXFind_ByNameAndRole(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "About", "link")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 link named 'About', got %d", len(nodes))
	}
}

func TestAXFind_NoResults(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "NonexistentThing", "")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 results for nonexistent name, got %d", len(nodes))
	}
}

func TestAXFind_FormPage(t *testing.T) {
	page := navigateTo(t, "/form")
	nodes, err := queryAXNodes(page, "", "textbox")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 textboxes on form page, got %d", len(nodes))
	}
}

// =====================
// ax-node tests (RED)
// =====================

func TestAXNode_ButtonBySelector(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#submit-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "button") {
		t.Errorf("should show role 'button', got:\n%s", out)
	}
	if !strings.Contains(out, "Submit") {
		t.Errorf("should show name 'Submit', got:\n%s", out)
	}
}

func TestAXNode_DisabledButton(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#cancel-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "button") {
		t.Errorf("should show role 'button', got:\n%s", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("should show disabled property, got:\n%s", out)
	}
}

func TestAXNode_InputWithLabel(t *testing.T) {
	page := navigateTo(t, "/form")
	node, err := getAXNode(page, "#name-input")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "textbox") {
		t.Errorf("should show role 'textbox', got:\n%s", out)
	}
	if !strings.Contains(out, "Name") {
		t.Errorf("should show accessible name 'Name' from label, got:\n%s", out)
	}
}

func TestAXNode_HeadingLevel(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "h1")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "heading") {
		t.Errorf("should show role 'heading', got:\n%s", out)
	}
	if !strings.Contains(out, "level") {
		t.Errorf("should show level property for heading, got:\n%s", out)
	}
}

func TestAXNode_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#submit-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetailJSON(node)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\nOutput:\n%s", err, out)
	}
	if _, ok := parsed["nodeId"]; !ok {
		t.Error("JSON should contain nodeId field")
	}
}

func TestAXNode_SelectorNotFound(t *testing.T) {
	page := navigateTo(t, "/")
	// Use a short timeout so we don't block for 30s waiting for a nonexistent element
	shortPage := page.Timeout(2 * time.Second)
	_, err := getAXNode(shortPage, "#does-not-exist")
	if err == nil {
		t.Error("expected error for nonexistent selector, got nil")
	}
}

// =====================
// file command tests
// =====================

func TestFile_SetFileOnInput(t *testing.T) {
	page := navigateTo(t, "/upload")

	// Create a temp file to upload
	tmp, err := os.CreateTemp("", "roddy-test-*.txt")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("test content"))
	tmp.Close()

	el, err := page.Element("#file-input")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	if err := el.SetFiles([]string{tmp.Name()}); err != nil {
		t.Fatalf("SetFiles failed: %v", err)
	}

	// Wait for the change event to fire and check the file name
	page.MustWaitStable()
	nameEl, err := page.Element("#file-name")
	if err != nil {
		t.Fatalf("file-name element not found: %v", err)
	}
	text, _ := nameEl.Text()
	if text == "" {
		t.Error("expected file name to be set after SetFiles, got empty string")
	}
}

func TestFile_MultipleFiles(t *testing.T) {
	page := navigateTo(t, "/upload")

	tmp1, _ := os.CreateTemp("", "roddy-test1-*.txt")
	defer os.Remove(tmp1.Name())
	tmp1.Write([]byte("file 1"))
	tmp1.Close()

	tmp2, _ := os.CreateTemp("", "roddy-test2-*.txt")
	defer os.Remove(tmp2.Name())
	tmp2.Write([]byte("file 2"))
	tmp2.Close()

	el, err := page.Element("#file-input")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}

	// Setting files should not error even with multiple files
	if err := el.SetFiles([]string{tmp1.Name(), tmp2.Name()}); err != nil {
		t.Fatalf("SetFiles with multiple files failed: %v", err)
	}
}

// =====================
// download command tests
// =====================

func TestDownload_DataURL(t *testing.T) {
	// Test decoding a data: URL directly
	data, err := decodeDataURL("data:text/plain;base64,SGVsbG8gV29ybGQ=")
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_DataURL_URLEncoded(t *testing.T) {
	data, err := decodeDataURL("data:text/plain,Hello%20World")
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_InferFilename_URL(t *testing.T) {
	name := inferDownloadFilename("https://example.com/images/photo.png")
	if name != "photo.png" {
		t.Errorf("expected 'photo.png', got %q", name)
	}
}

func TestDownload_InferFilename_DataURL(t *testing.T) {
	name := inferDownloadFilename("data:image/png;base64,abc")
	if !strings.HasPrefix(name, "download") || !strings.Contains(name, ".png") {
		t.Errorf("expected 'download*.png', got %q", name)
	}
}

func TestDownload_FetchLink(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#file-link")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	href := el.MustAttribute("href")
	if href == nil {
		t.Fatal("expected href attribute")
	}

	// Fetch using JS in the page context, same as cmdDownload does
	js := fmt.Sprintf(`async () => {
		const resp = await fetch(%q);
		if (!resp.ok) throw new Error('HTTP ' + resp.status);
		const buf = await resp.arrayBuffer();
		const bytes = new Uint8Array(buf);
		let binary = '';
		for (let i = 0; i < bytes.length; i++) {
			binary += String.fromCharCode(bytes[i]);
		}
		return btoa(binary);
	}`, *href)
	result, err := page.Eval(js)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	data, err := base64.StdEncoding.DecodeString(result.Value.Str())
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_DataLinkElement(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#data-link")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	href := el.MustAttribute("href")
	if href == nil {
		t.Fatal("expected href attribute")
	}

	data, err := decodeDataURL(*href)
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_ImgSrc(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#test-img")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	src := el.MustAttribute("src")
	if src == nil {
		t.Fatal("expected src attribute")
	}
	if *src != "/testfile.txt" {
		t.Errorf("expected '/testfile.txt', got %q", *src)
	}
}

// =====================
// Directory-scoped sessions tests
// =====================

func TestExtractScopeArgs_NoFlags(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"open", "https://example.com"})
	if mode != scopeAuto {
		t.Errorf("expected scopeAuto, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlag(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"--local", "start"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 1 || remaining[0] != "start" {
		t.Errorf("expected [start], got %v", remaining)
	}
}

func TestExtractScopeArgs_GlobalFlag(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"--global", "open", "https://example.com"})
	if mode != scopeGlobal {
		t.Errorf("expected scopeGlobal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlagAfterCommand(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"open", "--local", "https://example.com"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LastFlagWins(t *testing.T) {
	mode, _ := extractScopeArgs([]string{"--local", "--global", "start"})
	if mode != scopeGlobal {
		t.Errorf("expected last flag (scopeGlobal) to win, got %v", mode)
	}
}

func TestResolveStateDir_Global(t *testing.T) {
	dir := resolveStateDir(scopeGlobal, "/some/working/dir")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".roddy")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_Local(t *testing.T) {
	dir := resolveStateDir(scopeLocal, "/some/working/dir")
	expected := filepath.Join("/some/working/dir", ".roddy")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_AutoPrefersLocal(t *testing.T) {
	// Create a temp directory with a .roddy/state.json to simulate local session
	tmpDir := t.TempDir()
	localRoddy := filepath.Join(tmpDir, ".roddy")
	os.MkdirAll(localRoddy, 0755)
	os.WriteFile(filepath.Join(localRoddy, "state.json"), []byte(`{}`), 0644)

	dir := resolveStateDir(scopeAuto, tmpDir)
	if dir != localRoddy {
		t.Errorf("auto mode should prefer local when .roddy/state.json exists: expected %q, got %q", localRoddy, dir)
	}
}

func TestResolveStateDir_AutoFallsBackToGlobal(t *testing.T) {
	// Use a temp directory with NO .roddy/ — should fall back to global
	tmpDir := t.TempDir()
	dir := resolveStateDir(scopeAuto, tmpDir)
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".roddy")
	if dir != expected {
		t.Errorf("auto mode should fall back to global: expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_LocalUsesWorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	dir := resolveStateDir(scopeLocal, tmpDir)
	expected := filepath.Join(tmpDir, ".roddy")
	if dir != expected {
		t.Errorf("local mode should use working dir: expected %q, got %q", expected, dir)
	}
}

// =====================
// RODDY_HOME env var tests
// =====================

func TestStateDir_Default(t *testing.T) {
	t.Setenv("RODDY_HOME", "")
	home, _ := os.UserHomeDir()
	want := home + "/.roddy"
	got := stateDir()
	if got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
}

func TestStateDir_EnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RODDY_HOME", dir)
	got := stateDir()
	if got != dir {
		t.Errorf("stateDir() = %q, want %q", got, dir)
	}
}

func TestMimeToExt(t *testing.T) {
	tests := []struct {
		mime string
		ext  string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"unknown/type", ""},
	}
	for _, tt := range tests {
		got := mimeToExt(tt.mime)
		if got != tt.ext {
			t.Errorf("mimeToExt(%q) = %q, want %q", tt.mime, got, tt.ext)
		}
	}
}

// =====================
// assert command tests
// =====================

func TestAssert_TruthyPass_String(t *testing.T) {
	page := navigateTo(t, "/")
	// document.title is "Test Page" which is truthy
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	// Should not be falsy
	switch raw {
	case "false", "0", "null", "undefined", `""`:
		t.Errorf("document.title should be truthy, got raw=%q", raw)
	}
	if result.Value.Str() != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", result.Value.Str())
	}
}

func TestAssert_TruthyPass_True(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (1 === 1); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "true" {
		t.Errorf("1 === 1 should be true, got %q", raw)
	}
}

func TestAssert_TruthyPass_Number(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (42); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw == "0" || raw == "false" || raw == "null" || raw == "undefined" || raw == `""` {
		t.Errorf("42 should be truthy, got raw=%q", raw)
	}
}

func TestAssert_TruthyFail_Null(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.querySelector(".nonexistent")); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "null" {
		t.Errorf("querySelector for nonexistent should return null, got %q", raw)
	}
}

func TestAssert_TruthyFail_False(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (false); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "false" {
		t.Errorf("false should be false, got %q", raw)
	}
}

func TestAssert_TruthyFail_Zero(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (0); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "0" {
		t.Errorf("0 should be 0, got %q", raw)
	}
}

func TestAssert_TruthyFail_EmptyString(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (""); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != `""` {
		t.Errorf("empty string should have JSON repr '\"\"', got %q", raw)
	}
}

func TestAssert_EqualityPass_Title(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	actual := result.Value.Str()
	if actual != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", actual)
	}
}

func TestAssert_EqualityPass_Count(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.querySelectorAll("button").length); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "2" {
		t.Errorf("expected 2 buttons, got %q", raw)
	}
}

func TestAssert_EqualityFail_WrongTitle(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	actual := result.Value.Str()
	if actual == "Wrong Title" {
		t.Error("title should NOT equal 'Wrong Title'")
	}
}

func TestAssert_EqualityPass_BoolString(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (1 === 1); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "true" {
		t.Errorf("1 === 1 should produce 'true', got %q", raw)
	}
}

func TestFormatJSValue(t *testing.T) {
	// js and assert both print through formatJSValue, so this covers both.
	page := navigateTo(t, "/")

	tests := []struct {
		expr     string
		expected string
	}{
		{`document.title`, "Test Page"}, // string unquoted
		{`1 + 2`, "3"},                  // number
		{`true`, "true"},                // boolean
		{`null`, "null"},                // null
		{`document.querySelectorAll("button").length`, "2"}, // number from DOM
		{`0/0`, "NaN"},      // no JSON encoding; would print null
		{`1/0`, "Infinity"}, // likewise
	}

	for _, tt := range tests {
		js := fmt.Sprintf(`() => { return (%s); }`, tt.expr)
		result, err := page.Eval(js)
		if err != nil {
			t.Fatalf("eval %q failed: %v", tt.expr, err)
		}

		// The same two steps cmdJS takes to print a result.
		v := remoteObjectValue(result)
		if actual := formatJSValue(v); actual != tt.expected {
			t.Errorf("expr %q: expected %q, got %q (raw=%q)", tt.expr, tt.expected, actual,
				v.JSON("", ""))
		}
	}
}

// =====================
// assert --message tests
// =====================

func TestParseAssertArgs_ExprOnly(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil, got %q", *expected)
	}
	if message != "" {
		t.Errorf("message should be empty, got %q", message)
	}
}

func TestParseAssertArgs_ExprAndExpected(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "Dashboard"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Dashboard" {
		t.Errorf("expected = %v, want %q", expected, "Dashboard")
	}
	if message != "" {
		t.Errorf("message should be empty, got %q", message)
	}
}

func TestParseAssertArgs_MessageLong(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "--message", "Page title check"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil for truthy with --message, got %q", *expected)
	}
	if message != "Page title check" {
		t.Errorf("message = %q, want %q", message, "Page title check")
	}
}

func TestParseAssertArgs_MessageShort(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "-m", "Title check"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil, got %q", *expected)
	}
	if message != "Title check" {
		t.Errorf("message = %q, want %q", message, "Title check")
	}
}

func TestParseAssertArgs_EqualityWithMessage(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "Dashboard", "--message", "Wrong page"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Dashboard" {
		t.Errorf("expected = %v, want %q", expected, "Dashboard")
	}
	if message != "Wrong page" {
		t.Errorf("message = %q, want %q", message, "Wrong page")
	}
}

func TestParseAssertArgs_MessageBeforeExpr(t *testing.T) {
	// --message can appear anywhere; positional args still work
	expr, expected, message := parseAssertArgs([]string{"-m", "Check", "document.title", "Home"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Home" {
		t.Errorf("expected = %v, want %q", expected, "Home")
	}
	if message != "Check" {
		t.Errorf("message = %q, want %q", message, "Check")
	}
}

func TestFormatAssertFail_TruthyNoMessage(t *testing.T) {
	got := formatAssertFail("null", nil, "")
	if got != "fail: got null" {
		t.Errorf("got %q, want %q", got, "fail: got null")
	}
}

func TestFormatAssertFail_TruthyWithMessage(t *testing.T) {
	got := formatAssertFail("null", nil, "User should be logged in")
	if got != "fail: User should be logged in (got null)" {
		t.Errorf("got %q, want %q", got, "fail: User should be logged in (got null)")
	}
}

func TestFormatAssertFail_EqualityNoMessage(t *testing.T) {
	expected := "Dashboard"
	got := formatAssertFail("Task Tracker", &expected, "")
	want := `fail: got "Task Tracker", expected "Dashboard"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAssertFail_EqualityWithMessage(t *testing.T) {
	expected := "Dashboard"
	got := formatAssertFail("Task Tracker", &expected, "Wrong page loaded")
	want := `fail: Wrong page loaded (got "Task Tracker", expected "Dashboard")`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// =====================
// parseStartArgs tests
// =====================

func TestParseStartArgs_NoFlags(t *testing.T) {
	opts, err := parseStartArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.ignoreCertErrors {
		t.Error("expected insecure=false with no flags")
	}
	if !opts.headless {
		t.Error("expected headless=true with no flags")
	}
}

func TestParseStartArgs_ShowFlag(t *testing.T) {
	opts, err := parseStartArgs([]string{"--show"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.ignoreCertErrors {
		t.Error("expected insecure=false")
	}
	if opts.headless {
		t.Error("expected headless=false when --show is passed")
	}
}

func TestParseStartArgs_InsecureFlag(t *testing.T) {
	opts, err := parseStartArgs([]string{"--insecure"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.ignoreCertErrors {
		t.Error("expected insecure=true when --insecure is passed")
	}
	if !opts.headless {
		t.Error("expected headless=true when only --insecure is passed")
	}
}

func TestParseStartArgs_InsecureShortFlag(t *testing.T) {
	opts, err := parseStartArgs([]string{"-k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.ignoreCertErrors {
		t.Error("expected insecure=true when -k is passed")
	}
}

func TestParseStartArgs_ShowAndInsecure(t *testing.T) {
	opts, err := parseStartArgs([]string{"--show", "--insecure"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.ignoreCertErrors {
		t.Error("expected insecure=true")
	}
	if opts.headless {
		t.Error("expected headless=false when --show is passed")
	}
}

func TestParseStartArgs_UnknownFlag(t *testing.T) {
	_, err := parseStartArgs([]string{"--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag --bogus")
	}
	if !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("error should mention the unknown flag, got: %v", err)
	}
}

func TestParseStartArgs_NoSandboxFlag(t *testing.T) {
	opts, err := parseStartArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.noSandbox {
		t.Error("expected noSandbox=false with no flags")
	}
	opts, err = parseStartArgs([]string{"--no-sandbox"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.noSandbox {
		t.Error("expected noSandbox=true when --no-sandbox is passed")
	}
}

// sandbox defaults
// ================

func TestLaunchUnsandboxed(t *testing.T) {
	cases := []struct {
		flag        bool
		euid        int
		inContainer bool
		want        bool
		wantReason  bool // a note is printed only when the environment decided
	}{
		{false, 501, false, false, false}, // the default: sandboxed
		{true, 501, false, true, false},   // --no-sandbox
		{false, 0, false, true, true},     // root: Chrome refuses to sandbox at all
		{true, 0, false, true, false},     // the flag already said so: no note
		{false, 501, true, true, true},    // container: no kernel support to sandbox with
		{true, 501, true, true, false},
		{false, 0, true, true, true},
	}
	for _, c := range cases {
		got, reason := launchUnsandboxed(c.flag, c.euid, c.inContainer)
		if got != c.want {
			t.Errorf("launchUnsandboxed(%v, %d, %v) = %v, want %v", c.flag, c.euid, c.inContainer, got, c.want)
		}
		if (reason != "") != c.wantReason {
			t.Errorf("launchUnsandboxed(%v, %d, %v) reason = %q, want non-empty: %v",
				c.flag, c.euid, c.inContainer, reason, c.wantReason)
		}
	}
}

func TestSandboxLaunchError(t *testing.T) {
	// Lines a healthy Chrome prints; rod hands the whole buffer to the error,
	// so any failure of a Chrome that got as far as starting carries the
	// generic words "sandbox" and "zygote".
	const noise = "[WARNING:sandbox_linux.cc(393)] InitializeSandbox() called with multiple threads in process gpu-process.\n" +
		"[ERROR:zygote_host_impl_linux.cc(90)] Failed to adjust OOM score of renderer with pid 42: Permission denied.\n"

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"unshare denied",
			errors.New("Failed to get the debug url: " + noise +
				"[FATAL:zygote_host_impl_linux.cc(200)] Check failed: . : Failed to move to new namespace: PID namespaces supported, Network namespace supported, but failed: errno = Operation not permitted"),
			true,
		},
		{
			"no usable sandbox",
			errors.New("Failed to get the debug url: " + noise +
				"[FATAL:zygote_host_impl_linux.cc(107)] No usable sandbox! You need to build a kernel with CONFIG_USER_NS enabled"),
			true,
		},
		{
			"suid helper misconfigured",
			errors.New("Failed to get the debug url: The SUID sandbox helper binary was found, but is not configured correctly"),
			true,
		},
		{
			"root without the flag",
			errors.New("Failed to get the debug url: " + noise +
				"[FATAL:zygote_host_impl_linux.cc(120)] Running as root without --no-sandbox is not supported. See https://crbug.com/638180."),
			true,
		},
		{
			"zygote fork failure",
			errors.New("Failed to get the debug url: " + noise + "Zygote could not fork: process_type renderer"),
			true,
		},
		{
			"an unrelated crash after the routine sandbox noise",
			errors.New("Failed to get the debug url: " + noise +
				"[FATAL:video_capture_device_factory_apple.mm(37)] Check failed: mode."),
			false,
		},
		{
			"a stale profile lock, noise included",
			errors.New("Failed to get the debug url: " + noise + "SingletonLock: file exists"),
			false,
		},
		{
			// Deliberate: this line also appears when the zygote binary is
			// missing or unexecutable, so it is not evidence about the sandbox
			// and must not downgrade the session.
			"a zygote that never launched",
			errors.New("Failed to get the debug url: " + noise + "Failed to launch zygote process"),
			false,
		},
		{"a missing binary", errors.New("exec: \"/nope/chrome\": file does not exist"), false},
	}
	for _, c := range cases {
		if got := sandboxLaunchError(c.err); got != c.want {
			t.Errorf("sandboxLaunchError(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLaunchWithFallback(t *testing.T) {
	sandboxErr := errors.New("Failed to move to new namespace")
	otherErr := errors.New("SingletonLock: file exists")
	retryErr := errors.New("no such file or directory")

	cases := []struct {
		name            string
		unsandboxed     bool
		results         []error // one per expected call, in order
		wantCalls       []bool  // the unsandboxed argument each call must receive
		wantErr         error
		wantUnsandboxed bool
		wantWarn        string // substring, "" means nothing written
	}{
		{
			name:      "success launches once",
			results:   []error{nil},
			wantCalls: []bool{false},
		},
		{
			name:            "sandbox-shaped error retries unsandboxed",
			results:         []error{sandboxErr, nil},
			wantCalls:       []bool{false, true},
			wantUnsandboxed: true,
			wantWarn:        sandboxErr.Error(),
		},
		{
			name:      "other errors surface without a retry",
			results:   []error{otherErr},
			wantCalls: []bool{false},
			wantErr:   otherErr,
		},
		{
			name:            "an unsandboxed attempt has nothing to fall back to",
			unsandboxed:     true,
			results:         []error{sandboxErr},
			wantCalls:       []bool{true},
			wantErr:         sandboxErr,
			wantUnsandboxed: true,
		},
		{
			name:            "both fail: the retry's error is the one returned",
			results:         []error{sandboxErr, retryErr},
			wantCalls:       []bool{false, true},
			wantErr:         retryErr,
			wantUnsandboxed: true,
			wantWarn:        sandboxErr.Error(),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A distinct launcher and URL per call: returning the failed first
			// attempt's pair would write a dead PID into the state file.
			var calls []bool
			var launchers []*launcher.Launcher
			launch := func(unsandboxed bool) (*launcher.Launcher, string, error) {
				calls = append(calls, unsandboxed)
				if len(calls) > len(c.results) {
					t.Fatalf("launch called %d times, want %d", len(calls), len(c.results))
				}
				l := launcher.New()
				launchers = append(launchers, l)
				return l, fmt.Sprintf("ws://attempt-%d", len(calls)), c.results[len(calls)-1]
			}
			var warn bytes.Buffer
			l, u, unsandboxed, err := launchWithFallback(launch, c.unsandboxed, &warn)

			if !reflect.DeepEqual(calls, c.wantCalls) {
				t.Fatalf("launch calls = %v, want %v", calls, c.wantCalls)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
			if unsandboxed != c.wantUnsandboxed {
				t.Errorf("unsandboxed = %v, want %v", unsandboxed, c.wantUnsandboxed)
			}
			// Whatever the outcome, the LAST attempt's launcher and URL are the
			// ones describing the process that is (or is not) now running.
			last := len(calls) - 1
			if l != launchers[last] || u != fmt.Sprintf("ws://attempt-%d", last+1) {
				t.Errorf("result = %p, %q; want attempt %d's", l, u, last+1)
			}
			if c.wantWarn == "" {
				if warn.Len() > 0 {
					t.Errorf("unexpected warning: %q", warn.String())
				}
			} else if !strings.Contains(warn.String(), c.wantWarn) {
				t.Errorf("warning = %q, want it to mention %q", warn.String(), c.wantWarn)
			}
		})
	}
}

// A sandboxed launch must DELETE --no-sandbox, not merely skip setting it:
// rod's launcher.New() seeds it inside containers, where a test that only
// checks a fresh launcher would pass without exercising anything.
func TestApplySandboxFlags_DeletesContainerSeededNoSandbox(t *testing.T) {
	l := applySandboxFlags(launcher.New().Set("no-sandbox"), false)
	if l.Has("no-sandbox") {
		t.Error("--no-sandbox survived a sandboxed launch")
	}
	if l.Has("single-process") {
		t.Error("--single-process set on a sandboxed launch")
	}
}

func TestApplySandboxFlags_Unsandboxed(t *testing.T) {
	l := applySandboxFlags(launcher.New(), true)
	if !l.Has("no-sandbox") {
		t.Error("--no-sandbox missing from an unsandboxed launch")
	}
	if got, want := l.Has("single-process"), singleProcessSupported(); got != want {
		t.Errorf("single-process = %v, want %v (platform: %v)", got, want, runtime.GOOS)
	}
}

func TestNewStartLauncher_SandboxedByDefault(t *testing.T) {
	l := newStartLauncher(t.TempDir(), true, false, nil)
	if l.Has("no-sandbox") {
		t.Error("--no-sandbox set on a default launch")
	}
	if l.Has("single-process") {
		t.Error("--single-process set on a default launch")
	}
}

func TestNewStartLauncher_Unsandboxed(t *testing.T) {
	l := newStartLauncher(t.TempDir(), true, true, nil)
	if !l.Has("no-sandbox") {
		t.Error("--no-sandbox missing from an unsandboxed launch")
	}
	if got, want := l.Has("single-process"), singleProcessSupported(); got != want {
		t.Errorf("single-process = %v, want %v (platform: %v)", got, want, runtime.GOOS)
	}
}

// Extensions break under --single-process, so configureExtensions deletes it —
// which only works while it runs AFTER applySandboxFlags in newStartLauncher.
func TestNewStartLauncher_ExtensionsOutrankSingleProcess(t *testing.T) {
	l := newStartLauncher(t.TempDir(), true, true, []extensionInfo{{Dir: "/tmp/ext"}})
	if l.Has("single-process") {
		t.Error("--single-process survived loading an extension")
	}
	if got := headlessMode(l); got != "new" {
		t.Errorf("headless = %q, want %q", got, "new")
	}
}

// TestSandboxedLaunch is the test that matters for the sandbox default: a
// plain start must come up with Chrome's sandbox intact and still drive a
// page. Environments whose kernel cannot sandbox Chrome — root, containers,
// gVisor — are the fallback's job, so they skip rather than fail.
func TestSandboxedLaunch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: Chrome cannot sandbox itself")
	}
	l := newStartLauncher(t.TempDir(), true, false, nil)
	u, err := l.Launch()
	if err != nil {
		// Probe rather than read the message: an unsandboxed launch that comes
		// up proves the environment, not roddy, is what refuses the sandbox.
		if probeErr := probeUnsandboxedLaunch(t, t.TempDir()); probeErr != nil {
			t.Fatalf("sandboxed launch failed: %v; unsandboxed launch also failed: %v", err, probeErr)
		}
		// macOS sandboxes Chrome without kernel configuration, so there is no
		// environment left to blame: it is roddy's sandboxed flag set.
		if runtime.GOOS == "darwin" {
			t.Fatalf("sandboxed launch failed where the sandbox always works: %v", err)
		}
		t.Skipf("kernel cannot sandbox Chrome here (or roddy's sandboxed flag set is broken): %v", err)
	}
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		// Chrome is Leakless(false): a failure before the defer would orphan it.
		l.Kill()
		waitProcessGone(t, l.PID())
		t.Fatalf("connect to the sandboxed browser: %v", err)
	}
	defer func() {
		if err := browser.Close(); err != nil {
			l.Kill()
		}
		// Browser.close returns before the process is gone; wait for it or
		// t.TempDir races the profile teardown.
		waitProcessGone(t, l.PID())
	}()

	page := browser.MustPage(env.server.URL + "/")
	page.MustWaitLoad()
	if got := page.MustInfo().Title; got != "Test Page" {
		t.Errorf("title = %q, want %q", got, "Test Page")
	}
}

// waitProcessGone blocks until pid is gone, up to ~3s. rod's launcher runs its
// own cmd.Wait, so a second Wait here would lose the race and return
// immediately; signal 0 asks instead of reaping. On Windows Signal fails on
// the first call, which just ends the poll.
func waitProcessGone(t testing.TB, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Not fatal on its own; it explains a TempDir cleanup failure afterwards.
	t.Logf("Chrome (PID %d) still alive after 3s", pid)
}

// probeUnsandboxedLaunch launches and immediately closes an unsandboxed
// browser, reporting whether Chrome runs here at all. Chrome is launched
// Leakless(false), so every failure path after Launch must kill it.
func probeUnsandboxedLaunch(t testing.TB, dataDir string) error {
	t.Helper()
	l := newStartLauncher(dataDir, true, true, nil)
	u, err := l.Launch()
	if err != nil {
		return err
	}
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		l.Kill()
		waitProcessGone(t, l.PID())
		return err
	}
	err = browser.Close()
	if err != nil {
		l.Kill()
	}
	waitProcessGone(t, l.PID())
	return err
}

// proxy helper
// ============

// The property the stdin handoff exists for: argv carries no credential, since
// ps shows it to every user on the machine for the helper's whole lifetime.
func TestProxyHelperCommand_ArgvCarriesNoCredential(t *testing.T) {
	cmd := proxyHelperCommand("/usr/local/bin/roddy", "proxy.example:8080")
	want := []string{"/usr/local/bin/roddy", "_proxy", "proxy.example:8080"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %q, want %q", cmd.Args, want)
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "Basic ") {
			t.Errorf("argv carries a credential: %q", arg)
		}
	}
}

// errAfterPartialRead delivers data and a non-EOF error in the same Read, the
// shape a truncated credential arrives in.
type errAfterPartialRead struct {
	data string
	err  error
	done bool
}

func (r *errAfterPartialRead) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	n := copy(p, r.data)
	return n, r.err
}

func TestReadProxyAuthHeader(t *testing.T) {
	got, err := readProxyAuthHeader(strings.NewReader("Basic abc123\n"))
	if err != nil || got != "Basic abc123" {
		t.Errorf("readProxyAuthHeader = %q, %v; want %q, nil", got, err, "Basic abc123")
	}
	// A writer that closes without a newline still delivered the header.
	got, err = readProxyAuthHeader(strings.NewReader("Basic abc123"))
	if err != nil || got != "Basic abc123" {
		t.Errorf("readProxyAuthHeader (no newline) = %q, %v; want %q, nil", got, err, "Basic abc123")
	}
	if _, err := readProxyAuthHeader(strings.NewReader("")); err == nil {
		t.Error("expected an error for empty stdin")
	}
	if _, err := readProxyAuthHeader(strings.NewReader("\n")); err == nil {
		t.Error("expected an error for a blank line")
	}
	// A partial line delivered with a real error is truncated credentials, not
	// a header: the error has to win.
	broken := errors.New("read |0: file already closed")
	got, err = readProxyAuthHeader(&errAfterPartialRead{data: "Basic abc", err: broken})
	if !errors.Is(err, broken) || got != "" {
		t.Errorf("readProxyAuthHeader (partial + error) = %q, %v; want \"\", %v", got, err, broken)
	}
}

func TestAwaitProxyPort_ReturnsTheAnnouncedPort(t *testing.T) {
	r, w := announcePipe(t)
	go func() { _, _ = io.WriteString(w, "12345\n") }()
	port, err := awaitProxyPort(r, make(chan error), 5*time.Second)
	if err != nil {
		t.Fatalf("awaitProxyPort = %v, want port 12345", err)
	}
	if port != 12345 {
		t.Errorf("port = %d, want 12345", port)
	}
}

// A helper that is already gone has to be reported as gone, at once: waiting
// out the deadline for a process nothing can revive helps nobody.
func TestAwaitProxyPort_ReportsHelperExitAfterEOF(t *testing.T) {
	r, w := announcePipe(t)
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The status lands after the EOF, as cmd.Wait's does: a closed stdout must
	// not be reported as a deadline while the status is still on its way.
	exited := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		exited <- errors.New("exit status 2")
	}()

	start := time.Now()
	_, err := awaitProxyPort(r, exited, 30*time.Second)
	if err == nil {
		t.Fatal("awaitProxyPort = nil, want an exit error")
	}
	if !errors.Is(err, errProxyHelperExited) {
		t.Errorf("errors.Is(%q, errProxyHelperExited) = false, want true", err)
	}
	if !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("exit error = %q, want it to report the exit and the wait error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %s for an already-exited helper", elapsed)
	}
}

// The status can arrive while stdout is still open, and a nil one has to read
// as an exit rather than as a formatted <nil>.
func TestAwaitProxyPort_ReportsAHelperThatExitedCleanly(t *testing.T) {
	r, _ := announcePipe(t)
	exited := make(chan error, 1)
	exited <- nil

	_, err := awaitProxyPort(r, exited, 30*time.Second)
	if err == nil {
		t.Fatal("awaitProxyPort = nil, want an exit error")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("exit error = %q, want it to report the exit", err)
	}
	if strings.Contains(err.Error(), "nil") {
		t.Errorf("exit error = %q, want no formatting of the nil wait result", err)
	}
}

func TestAwaitProxyPort_DeadlineWhenNothingIsAnnounced(t *testing.T) {
	r, _ := announcePipe(t)
	deadline := 150 * time.Millisecond
	start := time.Now()
	_, err := awaitProxyPort(r, make(chan error), deadline)
	if err == nil {
		t.Fatal("awaitProxyPort = nil, want a deadline error")
	}
	if !strings.Contains(err.Error(), "150ms") {
		t.Errorf("deadline error = %q, want it to name the deadline", err)
	}
	if errors.Is(err, errProxyHelperExited) {
		t.Errorf("deadline error = %q, want it distinguishable from an exit", err)
	}
	if elapsed := time.Since(start); elapsed < deadline {
		t.Errorf("returned after %s, want at least the %s deadline", elapsed, deadline)
	}
}

func TestAwaitProxyPort_RejectsAnUnparsableAnnouncement(t *testing.T) {
	r, w := announcePipe(t)
	go func() { _, _ = io.WriteString(w, "Basic abc123\n") }()
	_, err := awaitProxyPort(r, make(chan error), 5*time.Second)
	if err == nil {
		t.Fatal("awaitProxyPort = nil, want a parse error")
	}
	if !strings.Contains(err.Error(), `"Basic abc123"`) {
		t.Errorf("parse error = %q, want it to quote the announced line", err)
	}
}

// announcePipe returns the two ends of an announce channel, closed at test end
// so awaitProxyPort's reading goroutine cannot outlive the test.
func announcePipe(t *testing.T) (*io.PipeReader, *io.PipeWriter) {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	return r, w
}

// The announcement is the whole handshake: it may not appear before the
// credential is in, or the parent would treat a listener with no credential as
// ready.
func TestRunProxyHelper_AnnouncesOnlyAfterTheHeader(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	announceR, announceW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runProxyHelper(ctx, "proxy.example:8080", stdinR, announceW) }()
	t.Cleanup(func() {
		cancel()
		_ = stdinW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runProxyHelper = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runProxyHelper did not return after the context was cancelled")
		}
		_ = announceR.Close()
	})

	announced := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(announceR).ReadString('\n')
		if err == nil {
			announced <- strings.TrimSpace(line)
		}
		close(announced)
	}()
	select {
	case line := <-announced:
		t.Fatalf("announced %q with stdin unwritten, want nothing until the header arrives", line)
	case <-time.After(300 * time.Millisecond):
	}

	if _, err := io.WriteString(stdinW, "Basic abc123\n"); err != nil {
		t.Fatalf("write auth header: %v", err)
	}
	var line string
	select {
	case l, ok := <-announced:
		if !ok {
			t.Fatal("announce channel closed without a port line")
		}
		line = l
	case <-time.After(10 * time.Second):
		t.Fatal("no port announced after the header was written")
	}
	port, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("announced %q, want a port: %v", line, err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial announced port %d: %v", port, err)
	}
	_ = conn.Close()
}

func TestRunProxyHelper_StdinClosedWithoutAHeader(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	var announce bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runProxyHelper(context.Background(), "proxy.example:8080", stdinR, &announce) }()
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no auth header arrived on stdin") {
			t.Errorf("runProxyHelper = %v, want it to report that no auth header arrived on stdin", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runProxyHelper did not return after stdin closed")
	}
	if announce.Len() != 0 {
		t.Errorf("announced %q without a credential, want nothing", announce.String())
	}
}

func TestStopProxyHelper_LeavesAReapedHelperAlone(t *testing.T) {
	exited := make(chan error, 1)
	exited <- nil
	var out bytes.Buffer
	// A negative PID keeps signalPID inert; only the decision is under test.
	stopProxyHelper(-1, exited, &out)
	if out.Len() != 0 {
		t.Errorf("stopProxyHelper printed %q for an exited helper, want nothing", out.String())
	}
}

func TestStopProxyHelper_StopsARunningHelper(t *testing.T) {
	var out bytes.Buffer
	stopProxyHelper(-1, make(chan error), &out)
	if !strings.Contains(out.String(), "stopping proxy helper") {
		t.Errorf("stopProxyHelper printed %q, want it to report stopping the helper", out.String())
	}
}

func TestProxyLogPath_LivesInStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RODDY_HOME", home)
	if got, want := proxyLogPath(), filepath.Join(home, "proxy.log"); got != want {
		t.Errorf("proxyLogPath() = %q, want %q", got, want)
	}
}

func TestProxyHelperFailure_InlinesTheHelperOutput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "proxy.log")
	if err := os.WriteFile(logPath, []byte("error: proxy listen failed: address already in use\nsecond line\n"), 0600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	msg := proxyHelperFailure(errProxyHelperExited, logPath)
	if !strings.Contains(msg, "exited before announcing a port") ||
		!strings.Contains(msg, "proxy listen failed: address already in use | second line") {
		t.Errorf("proxyHelperFailure = %q, want the startup error and the folded helper log", msg)
	}
	if strings.Contains(msg, "helper output: error: ") {
		t.Errorf("proxyHelperFailure = %q, want the helper's own \"error: \" prefix stripped", msg)
	}
	if !strings.Contains(msg, logPath) {
		t.Errorf("proxyHelperFailure = %q, want it to name %q", msg, logPath)
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("proxyHelperFailure = %q, want one line (fatal appends its own)", msg)
	}
}

func TestProxyHelperFailure_CapsTheInlinedOutput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "proxy.log")
	var log strings.Builder
	for i := 1; i <= proxyLogInlineLines+2; i++ {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	if err := os.WriteFile(logPath, []byte(log.String()), 0600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	msg := proxyHelperFailure(errors.New("boom"), logPath)
	if !strings.Contains(msg, fmt.Sprintf("line %d", proxyLogInlineLines)) {
		t.Errorf("proxyHelperFailure = %q, want the first %d lines", msg, proxyLogInlineLines)
	}
	if strings.Contains(msg, fmt.Sprintf("line %d", proxyLogInlineLines+1)) {
		t.Errorf("proxyHelperFailure = %q, want at most %d lines inlined", msg, proxyLogInlineLines)
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("proxyHelperFailure = %q, want the truncation marked", msg)
	}
}

func TestProxyHelperFailure_UnreadableLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "missing.log")
	_, readErr := os.ReadFile(logPath)
	if readErr == nil {
		t.Fatalf("reading %s succeeded, want it missing", logPath)
	}
	msg := proxyHelperFailure(errors.New("boom"), logPath)
	if !strings.Contains(msg, "boom") || !strings.Contains(msg, "unreadable") ||
		!strings.Contains(msg, logPath) || !strings.Contains(msg, readErr.Error()) {
		t.Errorf("proxyHelperFailure = %q, want the error, %q and why it is unreadable (%v)", msg, logPath, readErr)
	}
}

func TestProxyHelperFailure_EmptyLog(t *testing.T) {
	// Not named "empty.log": the path would satisfy the assertion by itself.
	logPath := filepath.Join(t.TempDir(), "proxy.log")
	if err := os.WriteFile(logPath, []byte("\n"), 0600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	msg := proxyHelperFailure(errors.New("boom"), logPath)
	if !strings.Contains(msg, "boom") || !strings.Contains(msg, "is empty") || !strings.Contains(msg, logPath) {
		t.Errorf("proxyHelperFailure (empty log) = %q, want the error, %q and that the log is empty", msg, logPath)
	}
}

func TestInsecureFlag_WithSelfSignedCert(t *testing.T) {
	// Create HTTPS server with self-signed certificate
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Secure Test</title></head>
<body><h1>HTTPS Test Page</h1></body></html>`))
	})
	httpsServer := httptest.NewUnstartedServer(mux)
	// Suppress expected TLS handshake errors to keep test output clean
	httpsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	httpsServer.StartTLS()
	defer httpsServer.Close()

	// Test 1: Browser WITHOUT --ignore-certificate-errors should fail
	t.Run("WithoutInsecureFlag", func(t *testing.T) {
		l := launcher.New().
			Set("no-sandbox").
			Set("disable-gpu").
			Set("single-process").
			Headless(true).
			Leakless(false)

		if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
			l = l.Bin(bin)
		}

		u := l.MustLaunch()
		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()

		page := browser.MustPage("")
		defer page.MustClose()

		err := page.Navigate(httpsServer.URL)
		if err == nil {
			t.Fatal("expected ERR_CERT_AUTHORITY_INVALID error, but navigation succeeded")
		}
		if !strings.Contains(err.Error(), "ERR_CERT_AUTHORITY_INVALID") {
			t.Errorf("expected ERR_CERT_AUTHORITY_INVALID, got: %v", err)
		}
	})

	// Test 2: Browser WITH --ignore-certificate-errors should succeed
	t.Run("WithInsecureFlag", func(t *testing.T) {
		l := launcher.New().
			Set("no-sandbox").
			Set("disable-gpu").
			Set("single-process").
			Set("ignore-certificate-errors"). // This is what --insecure sets
			Headless(true).
			Leakless(false)

		if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
			l = l.Bin(bin)
		}

		u := l.MustLaunch()
		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()

		// Try to navigate to HTTPS server with invalid cert
		page := browser.MustPage(httpsServer.URL)
		defer page.MustClose()

		page.MustWaitLoad()
		title := page.MustInfo().Title

		if title != "Secure Test" {
			t.Errorf("expected page to load successfully with title 'Secure Test', got %q", title)
		}
	})
}

// --- normalizeOpenURL ---

func TestNormalizeOpenURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.com", "http://example.com"},
		{"example.com/path?q=1", "http://example.com/path?q=1"},
		{"localhost:3000", "http://localhost:3000"},
		{"localhost:3000/app", "http://localhost:3000/app"},
		{"https://example.com", "https://example.com"},
		{"chrome-extension://abc/popup.html", "chrome-extension://abc/popup.html"},
		{"data:text/html,<title>t</title>", "data:text/html,<title>t</title>"},
		{"about:blank", "about:blank"},
		{"javascript:void(0)", "javascript:void(0)"},
		{"view-source:https://example.com", "view-source:https://example.com"},
		{"blob:https://example.com/uuid", "blob:https://example.com/uuid"},
	}
	for _, c := range cases {
		if got := normalizeOpenURL(c.in); got != c.want {
			t.Errorf("normalizeOpenURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// handleLogsPage serves a page that logs before any debugger attaches, so the
// log-stream tests can prove Chrome replays buffered console output. The failed
// fetch is reported by the Log domain rather than Runtime, which is what covers
// the second subscription.
func handleLogsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Logs Page</title></head>
<body>
  <script>
    console.log("fixture log", 7);
    console.warn("fixture warning");
    fetch("/missing-resource");
    setTimeout(() => { throw new Error("fixture boom"); }, 50);
  </script>
</body>
</html>`))
}

// --- adjustActivePage ---

func TestAdjustActivePage(t *testing.T) {
	cases := []struct {
		name                  string
		active, closed, count int
		want                  int
	}{
		// Closing a lower-indexed page shifts the rest down; the active page
		// keeps pointing at the same page only if the index follows it.
		{"close below active", 2, 0, 3, 1},
		{"close just below active", 2, 1, 3, 1},
		{"close above active", 1, 2, 3, 1},
		{"close the active page mid-list", 1, 1, 3, 1},
		{"close the active last page", 2, 2, 3, 1},
		{"active beyond new end", 1, 1, 2, 0},
		{"close above from zero", 0, 1, 2, 0},
		{"close below with two pages", 1, 0, 2, 0},
		{"close active of two", 0, 0, 2, 0},
		// A stale index (something else closed pages) self-heals into range.
		{"stale out-of-range", 9, 0, 3, 1},
		{"stale out-of-range, close tail", 9, 2, 3, 1},
	}
	for _, c := range cases {
		if got := adjustActivePage(c.active, c.closed, c.count); got != c.want {
			t.Errorf("%s: adjustActivePage(%d, %d, %d) = %d, want %d",
				c.name, c.active, c.closed, c.count, got, c.want)
		}
	}
}
