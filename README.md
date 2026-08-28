# Roddy: Chrome automation from the command line

[![Tests](https://github.com/goeric/roddy/actions/workflows/test.yml/badge.svg)](https://github.com/goeric/roddy/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](https://github.com/goeric/roddy/blob/main/LICENSE)

A Go CLI tool that drives a persistent headless Chrome instance using the [rod](https://github.com/go-rod/rod) browser automation library. Each command connects to the same long-running Chrome process, making it easy to script multi-step browser interactions from shell scripts or interactive use.

## About this fork

Roddy is a fork of [simonw/rodney](https://github.com/simonw/rodney) by Simon Willison, created because upstream development stalled. The last upstream commit was in March 2026, and pull requests — including several fixing crashes we hit daily — have sat unreviewed since February 2026.

This fork exists to keep those fixes available and installable. It is not a hostile fork: upstream pull requests are still open, and if Rodney resumes active development we would be glad to see this work land there instead.

### What Roddy adds over upstream

- **`--extension` flag** — load unpacked extension directories or packed `.crx`/`.zip` archives into a headless session, and list them with `roddy extensions`. ([upstream PR #52](https://github.com/simonw/rodney/pull/52))
- **Browser-process crash fixes** — stops Chromium running in-development features that abort the browser, and skips `--single-process` on macOS where it crashes on startup. ([upstream PR #54](https://github.com/simonw/rodney/pull/54))
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
roddy start              # Launch headless Chrome
roddy start --show       # Launch with visible browser window
roddy start --insecure   # Launch with TLS errors ignored (-k shorthand)
roddy connect host:9222  # Connect to existing Chrome on remote debug port
roddy status             # Show browser info and active page
roddy stop               # Shut down Chrome
```

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
  switches to automatically when `--extension` is used — the old headless mode
  cannot run extensions at all.
- Chrome only loads *unpacked* extensions from the command line, so a `.crx` is
  unpacked before being loaded. It therefore gets an ID derived from its path on
  disk rather than the ID it would have when installed from the Web Store.
- Only the extensions passed to `--extension` are enabled, and they stay loaded
  for the lifetime of the session.

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
guess rather than landing in whichever worker happens to be running. Flags may
appear anywhere in the command, before or after the expression.

Both forms wait up to `--timeout` (default 5s) for the workers to appear, which
covers the startup race after `roddy start`; `roddy sw` exits 1 when none are
running. The evaluation itself is bounded separately, by `ROD_TIMEOUT` (default
30s), so an expression whose promise never settles fails instead of hanging.

Waiting does not wake a worker Chrome has already suspended for idleness. Any
event the worker has a listener for restarts it, so the way to do that on
demand is to send it one: `chrome.runtime.sendMessage` from an extension page
dispatches an event that starts the worker.

This makes end-to-end extension tests plain shell — seed storage through the
worker, drive the page, assert on both sides:

```bash
# Testing a WXT project: build output is an unpacked MV3 extension
roddy start --extension .output/chrome-mv3

roddy sw eval 'chrome.storage.local.set({user: "test"}).then(() => "ok")'
roddy open https://example.com
roddy assert 'document.documentElement.dataset.myExtension' 'active'
roddy sw eval 'chrome.storage.local.get("lastSeen").then(v => v.lastSeen)'
```

Because Chrome derives an unpacked extension's ID from its load path — roddy
merely reproduces the computation to print the ID before launch — the ID is
stable across runs, so `chrome-extension://` URLs can be hardcoded in test
scripts instead of being discovered at runtime.

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
2. Launches a local forwarding proxy that injects `Proxy-Authorization` headers into CONNECT requests
3. Configures Chrome to use the local proxy

This is necessary because Chrome cannot natively authenticate to proxies during HTTPS tunnel (CONNECT) establishment. The local proxy runs as a background process and is automatically cleaned up by `roddy stop`.

See [claude-code-chrome-proxy.md](claude-code-chrome-proxy.md) for detailed technical notes.

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
| `start` | `[--show] [--insecure\|-k]` | Launch Chrome (headless by default, `--show` for visible) |
| `connect` | `<host:port>` | Connect to existing Chrome on remote debug port |
| `stop` | | Shut down Chrome |
| `status` | | Show browser status |
| `extensions` | | List extensions loaded into this session |
| `sw` | `[list] [--ext ID] [--timeout DUR]` | List extension service workers, waiting up to `--timeout` for them (exit 1 if none are running) |
| `sw eval` | `<expr> [--ext ID] [--timeout DUR]` | Evaluate JS inside an extension's service worker (the evaluation is bounded by `ROD_TIMEOUT`); flags may go either side of the expression |
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
