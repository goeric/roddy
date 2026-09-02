# Roddy: Chrome automation from the command line

[![Tests](https://github.com/goeric/roddy/actions/workflows/test.yml/badge.svg)](https://github.com/goeric/roddy/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](https://github.com/goeric/roddy/blob/main/LICENSE)

A Go CLI tool that drives a persistent headless Chrome instance using the [rod](https://github.com/go-rod/rod) browser automation library. Each command connects to the same long-running Chrome process, making it easy to script multi-step browser interactions from shell scripts or interactive use.

## About this fork

Roddy is a fork of [simonw/rodney](https://github.com/simonw/rodney) by Simon Willison, created because upstream development stalled. The last upstream commit was in March 2026, and pull requests — including several fixing crashes we hit daily — have sat unreviewed since February 2026.

This fork exists to keep those fixes available and installable. It is not a hostile fork: upstream pull requests are still open, and if Rodney resumes active development we would be glad to see this work land there instead.

### What Roddy adds over upstream

- **`--extension` flag** — load unpacked extension directories or packed `.crx`/`.zip` archives into a headless session, and list them with `roddy extensions`. ([upstream PR #52](https://github.com/simonw/rodney/pull/52))
- **Browser-process crash fixes** — stops Chromium running in-development features that abort the browser, and reserves `--single-process` for unsandboxed launches (containers, gVisor), never using it on macOS where it aborts the browser as soon as a page touches `navigator.mediaDevices`. ([upstream PR #54](https://github.com/simonw/rodney/pull/54))
- **Screenshot reliability** — raises the target before capturing, fixing captures that timed out when the page was not in the foreground. ([upstream PR #55](https://github.com/simonw/rodney/pull/55))

### Migrating from Rodney

The command, state directory, and environment variable are all renamed:

| Rodney | Roddy |
| --- | --- |
| `rodney` | `roddy` |
| `~/.rodney/`, `./.rodney/` | `~/.roddy/`, `./.roddy/` |
| `RODNEY_HOME` | `RODDY_HOME` |

Roddy does not read Rodney's state files. Run `rodney stop` before switching so the old browser process is shut down cleanly rather than orphaned; otherwise the commands and flags are unchanged.

## Installing

```bash
go install github.com/goeric/roddy@latest
```

Or build from a checkout:

```bash
go build -o roddy .
```

Requires:
- Go 1.21+
- Google Chrome or Chromium installed (or set `ROD_CHROME_BIN=/path/to/chrome`)

## Claude Code skill

This repo ships a [Claude Code](https://claude.com/claude-code) skill that
teaches Claude to reach for Roddy — instead of Playwright, Puppeteer, or the
Chrome DevTools MCP server — whenever a task needs a real browser: reproducing a
UI bug, inspecting the live DOM, reading console errors, filling a form, or
screenshotting a page.

Install it as a plugin:

```
/plugin marketplace add goeric/roddy
/plugin install roddy@roddy
```

Then restart Claude Code. The skill triggers on its own; you don't need to name
it. Update later with `/plugin update roddy`.

The skill documents the CLI, not just its existence — session lifecycle, the
check commands and their exit codes, extension loading, and the failure modes
worth recognising. It assumes `roddy` is on `PATH`, so install the binary first.

<details>
<summary>Installing the skill without plugins</summary>

The skill is a single self-contained file, so you can also symlink it into your
personal skills directory:

```bash
git clone https://github.com/goeric/roddy.git
mkdir -p ~/.claude/skills
ln -s "$PWD/roddy/skills/roddy" ~/.claude/skills/roddy
```

Restart Claude Code afterwards. Use one method or the other, not both — two
skills named `roddy` will both load and conflict.

</details>

## Architecture

```
roddy start          →  launches Chrome (headless, persists after CLI exits)
                          saves WebSocket debug URL to ~/.roddy/state.json

roddy connect H:P    →  connects to an existing Chrome on a remote debug port
                          saves WebSocket debug URL to ~/.roddy/state.json

roddy open URL       →  connects to running Chrome via WebSocket
                          navigates the active tab, disconnects

roddy js EXPR        →  connects, evaluates JS, prints result, disconnects

roddy stop           →  connects and shuts down Chrome, cleans up state
```

Each CLI invocation is a short-lived process. Chrome runs independently and tabs persist between commands.

## Usage

### Start/stop the browser

```bash
roddy start                                # Launch headless Chrome
roddy start --show                         # Launch with visible browser window
roddy start --insecure                     # Launch with certificate errors ignored (-k shorthand)
roddy start --no-sandbox                   # Launch without Chrome's sandbox (containers, gVisor)
roddy start --no-single-process            # Keep Chrome multi-process (crash isolation)
roddy start --no-sandbox --single-process  # Force single-process (gVisor with no marker files)
roddy connect host:9222                    # Connect to existing Chrome on remote debug port
roddy status                               # Show browser info and active page
roddy stop                                 # Shut down Chrome
```

Chrome runs with its sandbox on by default. Running as root implies
`--no-sandbox` — Chrome refuses to sandbox there — and so does a container
detected by its marker files or environment (Docker, Podman, Kubernetes, and
gVisor under any of them), where the kernel support Chrome's sandbox needs is
usually absent. Both are decided up front, with a note on stderr and no failed
first attempt; an unsandboxed launch also gets `--single-process` where the
platform takes it, which those environments need for screenshots — unless
extensions are loaded, which drops it again (it breaks them).

Site Isolation is on too: rod's launcher defaults turn it off, roddy turns it
back on, so cross-site frames get renderer processes of their own — a second
boundary behind the sandbox. It is moot under `--single-process`, where there is
only one process.

`--single-process` and `--no-single-process` override that derived default.
`--no-single-process` is always accepted, and is simply a no-op where the flag
was not going on anyway; it is how you get an unsandboxed *multi-process*
browser, for crash isolation. `--single-process` is refused rather than quietly
dropped when it cannot be honoured: on macOS (any page touching
`navigator.mediaDevices` aborts the browser), with extensions loaded (they break
under it — if the extension came from a WXT build `start` auto-loaded,
`--no-extension` opts out), and on a sandboxed launch (`--single-process
requires --no-sandbox`). A session that ends up single-process says "single
process" in `start` output and `status`.

The detection is a marker check, not a kernel probe, so containers it does not
recognise (LXC, systemd-nspawn, containerd outside Kubernetes) get the ordinary
sandboxed attempt. That attempt, if it fails *for a sandbox reason* — Chrome's
own stderr naming the cause, e.g. "Failed to move to new namespace" or "No
usable sandbox!" — retries once with `--no-sandbox` and says so; any other
failure is reported as-is rather than silently downgraded. `--no-sandbox` is
the explicit path when you already know the sandbox will not come up.

### Browser extensions

`--extension` loads a Chrome extension at startup. It works in headless mode as
well as with `--show`:

```bash
# An unpacked extension directory
roddy start --extension ./my-extension

# A packed .crx or .zip, which is unpacked into the session directory
roddy start --extension ./my-extension.crx

# Repeat the flag to load more than one
roddy start --extension ./one --extension ./two
```

`roddy extensions` lists what was loaded, including the ID Chrome assigned to
each one, which you need to reach pages served by the extension:

```bash
roddy extensions
# ldmakemplfmadpiihagnajidjbhnjlcm  My Extension  1.0.0  /path/to/my-extension

roddy open chrome-extension://ldmakemplfmadpiihagnajidjbhnjlcm/popup.html
roddy screenshot popup.png
```

Notes:

- Extensions run in Chrome's [new headless
  mode](https://developer.chrome.com/docs/chromium/new-headless), which roddy
  switches to automatically when extensions are loaded — the old headless mode
  cannot run extensions at all.
- Chrome only loads *unpacked* extensions from the command line, so a `.crx` is
  unpacked before being loaded. It therefore gets an ID derived from its path on
  disk rather than the ID it would have when installed from the Web Store.
- Only the extensions loaded at `start` — via `--extension`, or the WXT
  auto-detection below — are enabled, and they stay loaded for the lifetime of
  the session.

#### WXT projects load themselves

In a [WXT](https://wxt.dev) project — a `wxt.config.*` in the current
directory — plain `roddy start` auto-loads the built extension from
`.output/chrome-mv3`, printing a notice:

```bash
roddy start
# WXT project detected: loading .output/chrome-mv3 (pass --no-extension to skip)
# tip: use --local to keep this project's browser state isolated (./.roddy/)
# Chrome started (PID 12345)
# Debug URL: http://127.0.0.1:53421/devtools/browser/2f1c...
# Extension loaded: My Extension (ldmakemplfmadpiihagnajidjbhnjlcm)
```

Pass `--no-extension` to start without it, or `--extension PATH` to load
something else — an explicit choice always wins over detection. If the config
is there but `.output/chrome-mv3` is not, start says so and continues without
an extension: run `wxt build` and start again. A build that is there but cannot
be loaded — mid-write, no manifest, unreadable — prints why on stderr and start
continues without it, so detection never turns a working `roddy start` into an
error.

### Extension service workers

An MV3 extension's logic lives in its background service worker, which has no
DOM and is invisible to page-level commands. `roddy sw` reaches inside it:

```bash
roddy sw                    # list extension service workers
# ldmakemplfmadpiihagnajidjbhnjlcm  chrome-extension://ldmakemplfmadpiihagnajidjbhnjlcm/sw.js

# Evaluate in the worker's context — chrome.* APIs work, promises are awaited
roddy sw eval 'chrome.storage.local.get(null)'
roddy sw eval 'chrome.storage.local.set({flag: true}).then(() => "seeded")'
roddy sw eval 'chrome.runtime.getManifest().version'
```

With several extensions loaded, pick one with `--ext ID` — `sw eval` refuses to
guess rather than landing in whichever worker happens to be running. Only
extensions that declare a `background.service_worker` count towards that, so a
content-script-only extension alongside never forces the choice. Flags may
appear anywhere in the command, before or after the expression; put `--` in
front of an expression that itself starts with `--`.

Both forms wait up to `--timeout` (default 5s) for the workers to appear — one
per extension that declares one — which covers the startup race after `roddy
start`; `roddy sw` exits 1 when none are running. The evaluation itself is
bounded separately, by `ROD_TIMEOUT` (default 30s), so an expression whose
promise never settles fails instead of hanging.

Waiting does not wake a worker Chrome has already suspended for idleness. Any
event the worker has a listener for restarts it, so the way to do that on
demand is to send it one: `chrome.runtime.sendMessage` from an extension page
dispatches an event that starts the worker.

This makes end-to-end extension tests plain shell — seed storage through the
worker, drive the page, assert on both sides:

```bash
# Testing a WXT project: plain start auto-loads .output/chrome-mv3
roddy start

roddy storage set user test
roddy open https://example.com
roddy assert 'document.documentElement.dataset.myExtension' 'active'
roddy storage get lastSeen
```

Because Chrome derives an unpacked extension's ID from its load path — roddy
merely reproduces the computation to print the ID before launch — the ID is
stable across runs, so `chrome-extension://` URLs can be hardcoded in test
scripts instead of being discovered at runtime.

### Extension storage

Seeding and reading `chrome.storage` is the most common extension test-setup
act, so it has first-class sugar — no JS required:

```bash
roddy storage get                   # the whole area as JSON
roddy storage get user              # one key
roddy storage set user test         # a bare word stays a string...
roddy storage set count 3           # ...valid JSON keeps its type
roddy storage set config '{"theme":"dark"}'
roddy storage rm user count         # remove keys (absent ones are ignored)
roddy storage clear                 # wipe the area
```

`--area local|sync|session|managed` picks the storage area (default `local`).
`managed` is read-only by design, so writing to it reports Chrome's own error.
VALUE is parsed as JSON when it parses — `3`, `true`, `{"a":1}` — and treated
as a plain string otherwise; force a string that would otherwise parse by
quoting it twice: `roddy storage set flag '"true"'`.

`storage get KEY` of a key that is not there prints `undefined` and exits 1.
Presence of the key decides, so a stored `null` or `false` still exits 0:

```bash
roddy storage get onboarded || roddy storage set onboarded true
```

The commands run inside the extension's service worker through the same
plumbing as `sw eval`: the extension's manifest must declare the `storage`
permission, `--ext ID` picks one extension when several are loaded, and
`--timeout` (default 5s) bounds the wait for the worker. Flags may appear
anywhere; put `--` in front of a KEY or VALUE that itself starts with a dash,
which a negative number (`-1.5`) does not need.

### Console output

`roddy logs` prints the console of the active page — including messages logged
**before** the command ran, because Chrome replays its console buffer to each
new debugging session. Uncaught exceptions (with stack traces) and
browser-generated entries such as network failures are included.

```bash
roddy logs                  # snapshot: replayed console output, then exit
roddy logs --timeout 10s    # collect for up to 10s (default 5s)
roddy logs --follow         # keep streaming live output until Ctrl+C
roddy logs --sw             # an extension service worker's console instead
roddy logs --sw --ext ID    # pick one extension when several are loaded
```

A service worker has no DOM, so its console is often the only way to see what
it did — `roddy logs --sw` right after `roddy start --extension` shows the
worker's startup output. A worker Chrome suspended and later restarted is a new
target with an empty buffer, so that early window is the reliable one.

Notes:

- Replayed object arguments print as `Object` — Chrome's buffer keeps no
  preview. Live output under `--follow` shows a one-level preview like
  `{a: 1, b: "x"}`.
- A snapshot sorts what it collects by timestamp, because Chrome replays the
  console buffer and the browser log buffer as two separate bursts. `--follow`
  prints events as they arrive, so its replayed prefix comes out as those two
  unsorted bursts before the live stream begins.
- `--timeout` (default 5s) bounds a snapshot: it returns once the output has
  been quiet for a moment, or once the timeout is up. A page that logs
  continuously hits the timeout, prints what it collected, and says so on
  stderr — `--follow` is the way to keep reading. With `--sw` the same flag
  also bounds the wait for the worker to appear.
- A page with an empty console prints nothing and exits 0. A stream that ends
  on its own — the tab was closed, or the browser went away — reports the
  reason on stderr and exits 2.

### Stubbing network requests

`roddy stub` intercepts requests from pages, content scripts, and extension
service workers, and answers them from a rules file, so tests run against
canned responses instead of live servers (a web app's own service worker is
out of scope — its traffic stays live, and the stub says so at startup):

```bash
roddy stub rules.json           # holds the session until Ctrl+C
# stub: 4 rules active (Ctrl+C to stop)
# GET https://api.example.com/user → fulfill 200 (rule 1)
# POST https://t.example.com/beacon → abort internetdisconnected (rule 2)
```

**Start the stub before opening the pages it should cover.** Interception
applies to documents committed after it starts, so a tab that was already open
keeps the live network — no rules, no log lines — until it is reloaded
(`roddy reload`). The command says so on stderr when it finds open pages:

```
note: 2 open page(s) keep the live network until reloaded — start the stub first, or roddy reload
```

The rules file is a JSON array; the first matching rule wins, top to bottom,
and requests no rule matches continue to the real network untouched:

```json
[
  {"url": "**/api/user",      "fulfill": {"json": {"name": "test"}}},
  {"url": "**/telemetry/**",  "abort": "internetdisconnected"},
  {"url": "**/api/**", "method": "POST", "fulfill": {"status": 500, "body": "boom"}},
  {"url": "**/big-fixture",   "fulfill": {"path": "fixture.json"}}
]
```

The conventions are Playwright's, so patterns and verbs copy verbatim from
Playwright tests: URL globs where `*` stays inside a path segment, `**`
crosses segments, `{a,b}` alternates and `?` is a literal; verbs `fulfill`
(`status` default 200, `headers`, `contentType`, and at most one of `body` /
`json` / `path`, the last resolved relative to the rules file), `abort` (a
Playwright error code such as `internetdisconnected` or `connectionrefused`,
or `true` for `failed`), and `continue`. Everything is validated up front — a
typo in a rule fails the command before the browser is touched.

Details that make stubbed tests behave:

- Extension service workers get their own interception, armed before each
  worker runs its first instruction — a worker's own fetches bypass
  browser-wide interception, and its request path is fixed the moment it
  starts. Workers already running when the stub starts are therefore
  restarted (their in-memory state resets; stored state is untouched), and
  workers stay awake while the stub runs.

- Globs match the URL exactly as Chrome reports it; Playwright's base-URL
  normalization is not ported, so write a fully-qualified pattern the way the
  request really appears (scheme, host, port included) or start it with `**`.
- CORS preflights are answered with a synthesized 204 when the request they
  announce would be fulfilled or aborted — the real server may not exist — and
  passed through otherwise, so genuine CORS behavior stays testable. That
  policy governs: a rule of your own matching `OPTIONS` is not consulted for a
  real preflight (it still matches a plain `OPTIONS` request).
- Cross-origin fulfills get `Access-Control-Allow-Origin` reflected
  automatically unless the rule sets its own.
- Redirect hops pass through without re-matching (only the first request in a
  chain is stubbed), and interception bypasses the HTTP cache, so every
  request actually reaches the rules.
- `--verbose` (`-v`) logs the requests no rule matched as well. That is the way
  to debug a rule that is not firing: the request is there, with the exact URL
  and method your glob has to match.
- Stopping the stub (Ctrl+C) releases interception. Run one stub at a time:
  a second one does not warn, it chains behind the first in enable order, and
  the older holder's fulfills and aborts are the ones that win.

Run it in the background from a test script and stop it when done:

```bash
roddy stub rules.json & STUB=$!
roddy open https://example.com
roddy storage get lastSync         # the extension saw the stubbed API
kill $STUB
```

### Navigate

```bash
roddy open https://example.com    # Navigate to URL
roddy open example.com            # http:// prefix added automatically
roddy back                        # Go back
roddy forward                     # Go forward
roddy reload                      # Reload page
roddy reload --hard               # Reload bypassing cache
roddy clear-cache                 # Clear the browser cache
```

### Extract information

```bash
roddy url                    # Print current URL
roddy title                  # Print page title
roddy text "h1"              # Print text content of element
roddy html "div.content"     # Print outer HTML of element
roddy html                   # Print full page HTML
roddy attr "a#link" href     # Print attribute value
roddy pdf output.pdf         # Save page as PDF
```

### Run JavaScript

```bash
roddy js document.title                        # Evaluate expression
roddy js "1 + 2"                               # Math
roddy js 'document.querySelector("h1").textContent'  # DOM queries
roddy js '[1,2,3].map(x => x * 2)'            # Returns pretty-printed JSON
roddy js 'document.querySelectorAll("a").length'     # Count elements
```

The expression is automatically wrapped in `() => { return (expr); }`.

### Interact with elements

```bash
roddy click "button#submit"       # Click element
roddy input "#search" "query"     # Type into input field
roddy clear "#search"             # Clear input field
roddy file "#upload" photo.png    # Set file on a file input
roddy file "#upload" -            # Set file from stdin
roddy download "a.pdf-link"       # Download href/src target to file
roddy download "a.pdf-link" -     # Download to stdout
roddy select "#dropdown" "value"  # Select dropdown by value
roddy submit "form#login"         # Submit a form
roddy hover ".menu-item"          # Hover over element
roddy focus "#email"              # Focus element
```

### Wait for conditions

```bash
roddy wait ".loaded"       # Wait for element to appear and be visible
roddy waitload             # Wait for page load event
roddy waitstable           # Wait for DOM to stop changing
roddy waitidle             # Wait for network to be idle
roddy sleep 2.5            # Sleep for N seconds
```

### Screenshots

```bash
roddy screenshot                         # Save as screenshot.png
roddy screenshot page.png                # Save to specific file
roddy screenshot -w 1280 -h 720 out.png  # Set viewport width/height
roddy screenshot-el ".chart" chart.png   # Screenshot specific element
```

### Manage tabs

```bash
roddy pages                    # List all tabs (* marks active)
roddy newpage https://...      # Open URL in new tab
roddy page 1                   # Switch to tab by index
roddy closepage 1              # Close tab by index
roddy closepage                # Close active tab
```

### Query elements

```bash
roddy exists ".loading"    # Exit 0 if exists, exit 1 if not
roddy count "li.item"      # Print number of matching elements
roddy visible "#modal"     # Exit 0 if visible, exit 1 if not
roddy assert 'document.title' 'Home'  # Exit 0 if equal, exit 1 if not
roddy assert 'document.querySelector("h1") !== null'  # Exit 0 if truthy
```

### Accessibility testing

```bash
roddy ax-tree                           # Dump full accessibility tree
roddy ax-tree --depth 3                 # Limit tree depth
roddy ax-tree --json                    # Output as JSON

roddy ax-find --role button             # Find all buttons
roddy ax-find --name "Submit"           # Find by accessible name
roddy ax-find --role link --name "Home" # Combine filters
roddy ax-find --role button --json      # Output as JSON

roddy ax-node "#submit-btn"             # Inspect element's a11y properties
roddy ax-node "h1" --json               # Output as JSON
```

These commands use Chrome's [Accessibility CDP domain](https://chromedevtools.github.io/devtools-protocol/tot/Accessibility/) to expose what assistive technologies see. `ax-tree` uses `getFullAXTree`, `ax-find` uses `queryAXTree`, and `ax-node` uses `getPartialAXTree`.

```bash
# CI check: verify all buttons have accessible names
roddy ax-find --role button --json | python3 -c "
import json, sys
buttons = json.load(sys.stdin)
unnamed = [b for b in buttons if not b.get('name', {}).get('value')]
if unnamed:
    print(f'FAIL: {len(unnamed)} button(s) missing accessible name')
    sys.exit(1)
print(f'PASS: all {len(buttons)} buttons have accessible names')
"
```

### Directory-scoped sessions

By default, Roddy stores state globally in `~/.roddy/`. You can instead create a session scoped to the current directory with `--local`:

```bash
roddy start --local          # State stored in ./.roddy/state.json
                              # Chrome data in ./.roddy/chrome-data/
roddy open https://example.com   # Auto-detects local session
roddy stop                       # Cleans up local session
```

This is useful when you want isolated browser sessions per project — each directory gets its own Chrome instance, cookies, and state.

**Auto-detection:** When neither `--local` nor `--global` is specified, Roddy checks for `./.roddy/state.json` in the current directory. If found, it uses the local session; otherwise it falls back to the global `~/.roddy/` session.

```bash
# Force global even when a local session exists
roddy --global open https://example.com

# Force local (errors if no local session)
roddy --local status
```

The `--local` and `--global` flags can appear anywhere in the command:

```bash
roddy --local start
roddy start --local          # Same effect
roddy open --global https://example.com
```

Add `.roddy/` to your `.gitignore` to keep session state out of version control.

### Shell scripting examples

```bash
# Wait for page to load and extract data
roddy start
roddy open https://example.com
roddy waitstable
title=$(roddy title)
echo "Page: $title"

# Conditional logic based on element presence
if roddy exists ".error-message"; then
    roddy text ".error-message"
fi

# Loop through pages
for url in page1 page2 page3; do
    roddy open "https://example.com/$url"
    roddy waitstable
    roddy screenshot "${url}.png"
done

roddy stop
```

## Exit codes

Roddy uses distinct exit codes to separate check failures from errors:

| Exit code | Meaning |
|---|---|
| `0` | Success |
| `1` | Check failed — the command ran successfully but the condition/assertion was not met |
| `2` | Error — something went wrong (bad arguments, no browser session, timeout, etc.) |

This makes it easy to distinguish between "the assertion is false" and "the command couldn't run" in scripts and CI pipelines.

## Using Roddy for checks

Several commands return **exit code 1** when a condition is not met, making them useful as assertions in shell scripts and CI pipelines. All of these print their result to stdout and exit cleanly — no error message is written to stderr.

### `exists` — check if an element exists in the DOM

```bash
roddy exists "h1"
# Prints "true", exits 0

roddy exists ".nonexistent"
# Prints "false", exits 1
```

### `visible` — check if an element is visible

```bash
roddy visible "#modal"
# Prints "true" and exits 0 if the element exists and is visible

roddy visible "#hidden-div"
# Prints "false" and exits 1 if the element is hidden or doesn't exist
```

### `ax-find` — check for accessibility nodes

```bash
roddy ax-find --role button --name "Submit"
# Prints the matching node(s), exits 0

roddy ax-find --role banner --name "Nonexistent"
# Prints "No matching nodes" to stderr, exits 1
```

### `assert` — assert a JavaScript expression

With one argument, checks that the expression is truthy. With two arguments, checks that the expression's value equals the expected string. Use `--message` / `-m` to set a custom failure message.

```bash
# Truthy mode — check that expression evaluates to a truthy value
roddy assert 'document.querySelector(".logged-in") !== null'
# Prints "pass", exits 0

roddy assert 'document.querySelector(".nonexistent")'
# Prints "fail: got null", exits 1

# Equality mode — check that expression result matches expected value
roddy assert 'document.title' 'Dashboard'
# Prints "pass" if title is "Dashboard", exits 0

roddy assert 'document.querySelectorAll(".item").length' '3'
# Prints "pass" if there are exactly 3 items, exits 0

roddy assert 'document.title' 'Wrong Title'
# Prints 'fail: got "Dashboard", expected "Wrong Title"', exits 1
```

The expression is evaluated the same way as `roddy js` — the result is converted to its string representation before comparison. This means `roddy assert 'document.title' 'Dashboard'` compares the unquoted string, and `roddy assert '1 + 2' '3'` compares the number as a string.

Use `--message` (or `-m`) to add a human-readable description to the failure output:

```bash
roddy assert 'document.querySelector(".logged-in")' -m "User should be logged in"
# On failure: "fail: User should be logged in (got null)"

roddy assert 'document.title' 'Dashboard' --message "Wrong page loaded"
# On failure: 'fail: Wrong page loaded (got "Home", expected "Dashboard")'
```

### `storage get` — check an extension storage key

`roddy storage get KEY` prints the value when the key exists, and prints
`undefined` and exits 1 when it does not. The key's presence decides, so a
stored `null` or `false` still exits 0:

```bash
roddy storage get onboarded || roddy storage set onboarded true
```

### Combining checks in a shell script

You can chain these together in a single script to run multiple assertions. Because check failures use exit code 1 while real errors use exit code 2, you can use `set -e` to abort on errors while handling check failures explicitly:

```bash
#!/bin/bash
set -euo pipefail

FAIL=0

check() {
    if ! "$@"; then
        echo "FAIL: $*"
        FAIL=1
    fi
}

roddy start
roddy open "https://example.com"
roddy waitstable

# Assert elements exist
check roddy exists "h1"
check roddy exists "nav"
check roddy exists "footer"

# Assert key elements are visible
check roddy visible "h1"
check roddy visible "#main-content"

# Assert JS expressions
check roddy assert 'document.title' 'Example Domain'
check roddy assert 'document.querySelectorAll("p").length' '2'
check roddy assert 'document.querySelector("h1") !== null'

# Assert accessibility requirements
check roddy ax-find --role navigation
check roddy ax-find --role heading --name "Example Domain"

roddy stop

if [ "$FAIL" -ne 0 ]; then
    echo "Some checks failed"
    exit 1
fi
echo "All checks passed"
```

This pattern is useful in CI — run Roddy as a post-deploy check, an accessibility audit, or a smoke test against a staging environment. Because exit code 2 signals an actual error (e.g. Chrome didn't start), `set -e` will abort the script immediately if something is broken rather than reporting a misleading test failure.

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `RODDY_HOME` | `~/.roddy` | Data directory for state and Chrome profile |
| `ROD_CHROME_BIN` | `/usr/bin/google-chrome` | Path to Chrome/Chromium binary |
| `ROD_TIMEOUT` | `30` | Default timeout in seconds for element queries and for a `sw eval` |
| `HTTPS_PROXY` / `HTTP_PROXY` | (none) | Authenticated proxy auto-detected on start |

Global state is stored in `~/.roddy/state.json` with Chrome user data in `~/.roddy/chrome-data/`. When using `--local`, state is stored in `./.roddy/state.json` and `./.roddy/chrome-data/` in the current directory instead. Set `RODDY_HOME` to override the default global directory.

## Proxy support

In environments with authenticated HTTP proxies (e.g., `HTTPS_PROXY=http://user:pass@host:port`), `roddy start` automatically:

1. Detects the proxy credentials from environment variables
2. Launches a local forwarding proxy that injects `Proxy-Authorization` headers into CONNECT requests (the credentials are handed to it over stdin, so they are not in its argv, which `ps` shows to every user on the machine — its environment still carries `HTTPS_PROXY`, visible to the owner and root via `ps eww`)
3. Configures Chrome to use the local proxy on the port that proxy reports back

This is necessary because Chrome cannot natively authenticate to proxies during HTTPS tunnel (CONNECT) establishment. The local proxy runs as a background process and is automatically cleaned up by `roddy stop`.

`start` waits for that local proxy to report the port it bound; if the helper exits first or never reports one, `start` stops it and fails (exit 2) with the helper's own output in the error message. That output also lands in `proxy.log` under the state directory (`~/.roddy/`, or `./.roddy/` with `--local`), overwritten on each `start`.

### Certificate errors behind a proxy

The local helper is a transparent CONNECT tunnel: it never terminates TLS, so it originates no certificate error. A `net::ERR_CERT_*` behind a proxy is the target's own certificate or an upstream proxy that inspects TLS and re-signs with its own CA. `start` keeps certificate validation on that path, and `open` (and `newpage`) names both fixes when it hits one:

- Install that CA where Chrome reads it. On Linux that is the NSS shared DB — Chrome reads it from `~/.pki/nssdb` only, and ignores `--user-data-dir` for trust:

  ```bash
  mkdir -p "$HOME/.pki/nssdb"   # certutil fails if the directory is absent
  certutil -d sql:"$HOME/.pki/nssdb" -A -t "C,," -n <name> -i <ca.pem>
  roddy stop && roddy start     # a running Chrome does not pick up new anchors
  ```

  `certutil` ships in `libnss3-tools` (Debian/Ubuntu) or `nss-tools` (Fedora). On macOS the login keychain holds it, and importing without `-r trustRoot` trusts nothing:

  ```bash
  security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db <ca.pem>
  ```

- Or restart with `roddy start --insecure`, which ignores certificate errors for every page in the session — `start` and `status` then say "certificate errors ignored".

See [notes/claude-chrome-proxy/README.md](notes/claude-chrome-proxy/README.md) for detailed technical notes.

## How it works

The tool uses the [rod](https://github.com/go-rod/rod) Go library which communicates with Chrome via the DevTools Protocol (CDP) over WebSocket. Key implementation details:

- **`start`** uses rod's `launcher` package to start Chrome with `Leakless(false)` so Chrome survives after the CLI exits
- **Proxy auth** handled via a local forwarding proxy that bridges Chrome to authenticated upstream proxies
- **State persistence** via a JSON file containing the WebSocket debug URL and Chrome PID
- **Each command** creates a new rod `Browser` connection to the same Chrome instance, executes the operation, and disconnects
- **Element queries** use rod's built-in auto-wait with a configurable timeout (default 30s)
- **JS evaluation** wraps user expressions in arrow functions as required by rod's `Eval`
- **Accessibility commands** call CDP's Accessibility domain directly via rod's `proto` package (`getFullAXTree`, `queryAXTree`, `getPartialAXTree`)

## Dependencies

- [github.com/go-rod/rod](https://github.com/go-rod/rod) v0.116.2 - Chrome DevTools Protocol automation

## Commands reference

| Command | Arguments | Description |
|---|---|---|
| `start` | `[--show] [--insecure\|-k] [--no-sandbox] [--extension PATH] [--no-extension] [--single-process\|--no-single-process]` | Launch Chrome (headless by default, `--show` for visible) |
| `connect` | `<host:port>` | Connect to existing Chrome on remote debug port |
| `stop` | | Shut down Chrome |
| `status` | | Show browser status |
| `extensions` | | List extensions loaded into this session |
| `sw` | `[list] [--ext ID] [--timeout DUR]` | List extension service workers, waiting up to `--timeout` for them (exit 1 if none are running) |
| `sw eval` | `<expr> [--ext ID] [--timeout DUR]` | Evaluate JS inside an extension's service worker (the evaluation is bounded by `ROD_TIMEOUT`); flags may go either side of the expression |
| `storage` | `get [KEY] \| set KEY VALUE \| rm KEY... \| clear` | Read and write `chrome.storage` from inside an extension's service worker; `--area local\|sync\|session\|managed` (default local), plus `--ext` and `--timeout` as for `sw eval`, and `--` before a KEY or VALUE starting with a dash |
| `logs` | `[--follow\|-f] [--sw] [--ext ID] [--timeout DUR]` | Print console output (replayed + live) |
| `stub` | `<rules-file> [--verbose\|-v]` | Answer the browser's network requests from a JSON rules file, held in the foreground until Ctrl+C (`--verbose` also logs the requests no rule matched) |
| `open` | `<url>` | Navigate to URL |
| `back` | | Go back in history |
| `forward` | | Go forward in history |
| `reload` | `[--hard]` | Reload page (`--hard` bypasses cache) |
| `clear-cache` | | Clear the browser cache |
| `url` | | Print current URL |
| `title` | | Print page title |
| `html` | `[selector]` | Print HTML (page or element) |
| `text` | `<selector>` | Print element text content |
| `attr` | `<selector> <name>` | Print attribute value |
| `pdf` | `[file]` | Save page as PDF |
| `js` | `<expression>` | Evaluate JavaScript |
| `click` | `<selector>` | Click element |
| `input` | `<selector> <text>` | Type into input |
| `clear` | `<selector>` | Clear input |
| `file` | `<selector> <path\|->` | Set file on a file input (`-` for stdin) |
| `download` | `<selector> [file\|-]` | Download href/src target (`-` for stdout) |
| `select` | `<selector> <value>` | Select dropdown value |
| `submit` | `<selector>` | Submit form |
| `hover` | `<selector>` | Hover over element |
| `focus` | `<selector>` | Focus element |
| `wait` | `<selector>` | Wait for element to appear |
| `waitload` | | Wait for page load |
| `waitstable` | | Wait for DOM stability |
| `waitidle` | | Wait for network idle |
| `sleep` | `<seconds>` | Sleep N seconds |
| `screenshot` | `[-w N] [-h N] [file]` | Page screenshot (optional viewport size) |
| `screenshot-el` | `<selector> [file]` | Element screenshot |
| `pages` | | List tabs |
| `page` | `<index>` | Switch tab |
| `newpage` | `[url]` | Open new tab |
| `closepage` | `[index]` | Close tab |
| `exists` | `<selector>` | Check element exists (exit 1 if not) |
| `count` | `<selector>` | Count matching elements |
| `visible` | `<selector>` | Check element visible (exit 1 if not) |
| `assert` | `<expr> [expected] [-m msg]` | Assert JS expression is truthy or equals expected (exit 1 if not) |
| `ax-tree` | `[--depth N] [--json]` | Dump accessibility tree |
| `ax-find` | `[--name N] [--role R] [--json]` | Find accessible nodes |
| `ax-node` | `<selector> [--json]` | Show element accessibility info |

### Global flags

| Flag | Description |
|---|---|
| `--local` | Use directory-scoped session (`./.roddy/`) |
| `--global` | Use global session (`~/.roddy/`) |
| `--version` | Print version and exit |
| `--help`, `-h`, `help` | Show help message |
