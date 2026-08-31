---
name: roddy
description: >-
  Drive headless Chrome for debugging and browser automation using the `roddy`
  CLI instead of the Chrome DevTools MCP tools. Use this skill whenever a task
  involves a real browser — debugging a web page or web app, reproducing a UI
  bug, inspecting the live DOM, checking console errors, watching network
  requests, taking screenshots, filling forms, clicking through a flow, scraping
  rendered content, or verifying that a frontend change actually works in the
  browser. Trigger it even when the user says "open the page", "check it in
  Chrome", "screenshot the app", "why is this button broken", "debug the
  frontend", or "automate this site" without naming roddy. Strongly prefer
  roddy over the chrome-devtools MCP server / `mcp__*chrome-devtools*` tools and
  over Playwright/Puppeteer for these tasks — roddy is the preferred
  browser-automation tool here and is installed on PATH.
---

# roddy — headless Chrome from the command line

`roddy` (https://github.com/goeric/roddy) drives one persistent Chrome process
through simple shell commands. Every `roddy <cmd>` connects to the same
long-running browser, so you build up state across commands: `start` once,
`open` a page, poke at it with many small commands, then `stop`.

**Use roddy, not the Chrome DevTools MCP tools.** roddy is installed as the
browser-debugging tool for this environment. Reach for it for anything that
needs a real, rendered browser. Do not fall back to the `mcp__*chrome-devtools*`
tools, Playwright, or Puppeteer unless the user explicitly asks for them or
roddy genuinely cannot do the job (and say so first).

## The one gotcha that will bite you: `roddy js` evaluates an *expression*

`roddy js "<code>"` runs a single JavaScript **expression** and prints its
result. A statement list fails to parse — e.g. a leading `const` gives
`JS error: eval js error: SyntaxError: Unexpected token 'const'`. To run
multiple statements, wrap them in an immediately-invoked function so the whole
thing is one expression:

```bash
# WRONG — multiple statements, fails to parse
roddy js "const el = document.querySelector('#x'); el.textContent"

# RIGHT — one expression (IIFE), return the value you want
roddy js "(function(){ const el = document.querySelector('#x'); return el && el.textContent; })()"
```

Keep this in mind for every non-trivial `js` call below — the debugging recipes
rely on it.

## Lifecycle

```bash
roddy start              # launch headless Chrome (downloads its own Chromium on first run)
roddy start --show       # launch with a visible window (great for watching a repro)
roddy start --extension <path>   # load a Chrome extension (works headless — see below)
roddy start --insecure   # or -k: ignore certificate errors (self-signed dev servers)
roddy connect <host:port>        # attach to a Chrome already listening on a debug port
roddy status             # which pages? (but see the exit-code trap under Checks)
roddy stop               # shut it down
```

Start once at the beginning of a debugging session and `stop` when you're done
so you don't leave Chrome running. If `start` reports it's already running,
that's fine — reuse it.

## Sessions: how roddy stays "any project"

roddy auto-detects which browser session to talk to:

- If `./.roddy/state.json` exists in the current directory, it uses that
  **project-local** session.
- Otherwise it uses the **global** session at `~/.roddy/`.

Force it explicitly with `--local` (directory-scoped, `./.roddy/`) or
`--global` (`~/.roddy/`). Use `roddy start --local` inside a project when you
want that project's browser/cookies/login state isolated from everything else;
use the global session for quick one-off debugging. When in doubt for a
project-specific app, prefer `--local` so state doesn't leak between projects.

## Browser extensions

Extensions must be loaded **at `start` time** — there is no way to add one to a
running browser, so `roddy stop` and start again if you forget.

```bash
roddy start --extension ./my-extension            # unpacked directory
roddy start --extension ./packed.crx              # or a .crx / .zip (unpacked automatically)
roddy start --extension ./one --extension ./two   # repeat for several
roddy start                                       # in a WXT project: auto-loads .output/chrome-mv3
roddy extensions                                  # list what's loaded, with IDs
roddy sw eval '<js>'                              # run JS inside the background service worker
roddy storage get/set/rm/clear                    # chrome.storage without writing JS
roddy stub rules.json                             # answer network requests from canned rules
roddy logs [--sw]                                 # console output, incl. messages from before
```

### Extensions in headless mode

This works headless, not just with `--show` — content scripts run and MV3
background service workers start, so extension behaviour is fully testable
without a visible window.

The reason it works is worth knowing, because it changes how the browser runs.
Chrome's *old* headless mode is a separate, stripped-down browser that cannot
load extensions at all, so passing `--extension` silently switches the session
to **new headless** (`--headless=new`), which is the real browser with the
screen turned off. Consequences:

- The session runs multi-process (renderer, GPU, utility services, and the
  extension's own service worker each get a process). A plain `roddy start`
  uses old headless instead.
- Startup is a little slower and uses more memory than a plain session.
- You do not opt into this; `--extension` implies it. There is no flag to load
  an extension into old headless.

Verify which mode you actually got:

```bash
ps -ww -o command= -p "$(roddy status | sed -n 's/.*PID \([0-9]*\).*/\1/p')" \
  | tr ' ' '\n' | grep -- --headless      # "--headless=new" when an extension is loaded
```

### Reaching the service worker directly

`roddy sw eval` evaluates JS **inside** an MV3 extension's background service
worker — the context where `chrome.*` APIs live and the extension's real state
sits. Promises are awaited:

```bash
roddy sw                                                  # list workers: ID + sw.js URL
roddy sw eval 'chrome.storage.local.get(null)'            # read all extension storage
roddy sw eval 'chrome.storage.local.set({user:"t"}).then(() => "seeded")'
roddy sw eval 'chrome.runtime.getManifest().version'
```

With several extensions loaded, disambiguate with `--ext ID` — `sw eval`
refuses to guess, though only extensions declaring a `background.service_worker`
count. Flags may appear anywhere, before or after the expression. Both forms
wait up to `--timeout` (default 5s) for the workers to appear after `start` —
one per extension that declares one — and `roddy sw` exits 1 if none are
running; the evaluation itself is bounded by `ROD_TIMEOUT` (default 30s).
Neither wakes a worker Chrome suspended for idleness — send it a message to do
that, as shown below.

This is the backbone of extension e2e testing: seed state through the worker,
drive the page, assert on both sides. For a WXT project plain `roddy start` in
the project root is enough: when `wxt.config.*` and a built `.output/chrome-mv3`
are present it loads the extension and prints a notice (`--no-extension` opts
out, an explicit `--extension` always wins). If it reports the build output
missing, run `wxt build` first; if a build is there but cannot be loaded, start
prints why on stderr and continues without the extension.

### Extension storage without writing JS

`chrome.storage` reads and writes have first-class sugar — prefer it over
composing `sw eval` expressions by hand:

```bash
roddy storage get                        # whole area as JSON
roddy storage get user                   # one key; missing → "undefined", exit 1
roddy storage set user test              # bare word stays a string
roddy storage set count 3                # valid JSON keeps its type (3, true, {"a":1})
roddy storage set config '{"theme":"dark"}'
roddy storage rm user count              # absent keys are silently ignored
roddy storage clear                      # wipe the area
roddy storage get tok --area session     # areas: local (default), sync, session, managed
```

`storage get KEY` is a check like `exists`: presence decides the exit code, so
a stored `null` or `false` still exits 0 and `roddy storage get onboarded ||
roddy storage set onboarded true` seeds only when unseeded. To force a string
that would parse as JSON, quote it twice: `roddy storage set flag '"true"'`.
The commands run inside the service worker, so the extension's manifest must
declare the `storage` permission and the `--ext`/`--timeout` rules of `sw eval`
apply unchanged. Put `--` before a KEY or VALUE that starts with a dash.
`managed` is read-only by design — writes report Chrome's error.

Alternatively, a worker also responds to messages sent from any extension page —
and the message itself is an event, so this starts a suspended worker:

```bash
ID=$(roddy extensions | awk '{print $1}')
roddy open "chrome-extension://$ID/popup.html"
roddy js "chrome.runtime.sendMessage({type:'ping'})"     # returns the worker's reply
```

On ordinary sites the extension `chrome.*` APIs (`chrome.storage` and friends)
are unavailable to `roddy js` — from a normal page you can only observe what the
content script did to the DOM; use `roddy sw eval` for everything behind the
scenes.

To reach a page served by the extension you need the ID Chrome assigned it,
which `roddy extensions` prints as the first column:

```bash
ID=$(roddy extensions | awk '{print $1}')          # or | grep 'My Extension' | awk '{print $1}'
roddy open "chrome-extension://$ID/popup.html"
roddy screenshot /tmp/popup.png
```

Gotchas worth knowing:

- The ID of an unpacked extension is derived from its **path**, so it changes if
  you move or rebuild the extension to a different directory, and a `.crx`
  unpacked by roddy does *not* get the ID it would have from the Web Store.
  Always read the ID back from `roddy extensions` rather than hardcoding it.
- Only the extensions passed to `--extension` are enabled.
- An extension's service worker may open its own tab on install (onboarding
  pages commonly do), so the tab you want is not always page `[0]` — check
  `roddy pages` and switch with `roddy page <i>`.
- Point `--extension` at the **built** output (e.g. a WXT/Plasmo `.output/chrome-mv3`
  or webpack `dist/` directory), not the extension's source directory.

### Stubbing the network

`roddy stub rules.json` intercepts every request the browser makes — pages,
content scripts, and extension service workers — and answers matching ones
from a JSON rules file. It holds the session in the foreground until Ctrl+C,
printing one line per decision; run it in the background from scripts:

```bash
roddy stub rules.json & STUB=$!
roddy open https://example.com          # the page and the extension now see stubs
roddy sw eval 'fetch("https://api.example.com/user").then(r => r.json())'
kill $STUB                              # requests flow to the real network again
```

Rules use Playwright's conventions (globs where `**` crosses path segments and
`*` does not; verbs fulfill/abort/continue with Playwright's field names), so
a Playwright route translates mechanically:

```json
[
  {"url": "**/api/user",     "fulfill": {"json": {"name": "test"}}},
  {"url": "**/telemetry/**", "abort": "internetdisconnected"},
  {"url": "**/fixture",      "fulfill": {"path": "big.json", "status": 200}}
]
```

First match wins, top to bottom; unmatched requests continue to the real
network. Preflights for stubbed endpoints are answered automatically, and
cross-origin fulfills get CORS headers reflected — stub a cross-origin API
and the page can read it without server cooperation. The whole file is
validated before the browser is touched. One stub at a time.

## Reading console output

`roddy logs` prints the console of the active page, and `roddy logs --sw` an
extension service worker's — including output from **before** the command ran,
because Chrome replays its console buffer to each new debugging session. This
is the fastest way to see why a page or worker misbehaved after the fact:

```bash
roddy logs                   # what has this page logged? (errors include stacks)
roddy logs --follow          # stream live output until interrupted
roddy logs --sw              # what did the extension's worker log?
```

A worker has no DOM, so its console is often the only way to see what it did —
check `roddy logs --sw` before instrumenting anything, and check it early: a
worker Chrome suspended and restarted is a new target with an empty buffer.
Replayed object arguments print as `Object` (Chrome's buffer keeps no preview);
live output under `--follow` shows one-level previews.

A snapshot returns once the output goes quiet, or after `--timeout` (default 5s)
if the page never stops logging — in which case it prints what it collected and
says so on stderr. It sorts by timestamp; `--follow` cannot, so its replayed
prefix arrives as two unsorted bursts (console, then browser log entries) ahead
of the live stream.

## Command reference

Navigation & info:

```bash
roddy open <url>            # navigate (use file://… for local HTML)
roddy back / forward        # history
roddy reload [--hard]       # reload (note: wipes page JS state — see console capture below)
roddy url                   # current URL
roddy title                 # page title
roddy html [selector]       # full page HTML, or one element's outerHTML
roddy text <selector>       # an element's text content
roddy attr <selector> <name># an attribute value
```

Interaction:

```bash
roddy click <selector>
roddy input <selector> <text>     # type into an input
roddy clear <selector>            # empty an input
roddy select <selector> <value>   # pick a <select> option
roddy submit <selector>           # submit a form
roddy hover <selector>
roddy focus <selector>
roddy file <selector> <path|->    # set a file on a file input
```

Synchronizing (do this instead of guessing with sleeps):

```bash
roddy wait <selector>       # wait until an element appears
roddy waitload              # wait for page load
roddy waitstable            # wait for the DOM to stop changing
roddy waitidle              # wait for network to go idle
roddy sleep <seconds>       # last resort
```

Checks (these set exit codes — perfect for scripting a repro):

```bash
roddy exists <selector>             # exit 1 if absent
roddy visible <selector>            # exit 1 if not visible
roddy count <selector>              # print how many match
roddy assert <expr> [expected] [-m msg]   # assert a JS expression is truthy (or == expected)
roddy storage get <key>             # exit 1 if the extension storage key is absent
```

**Do not use `roddy status` as a liveness check.** It exits `0` even when the
browser is gone, printing `Browser not responding (PID N, state may be stale)`
on success-looking output. Probe with a real command instead, which exits `2`
when there is no browser:

```bash
roddy url >/dev/null 2>&1 || roddy start    # restart only if actually dead
```

Capture & misc:

```bash
roddy screenshot [-w N] [-h N] [file]   # full page; -h sets a viewport height and
                                         # switches to viewport-only capture
roddy screenshot-el <sel> [file]        # screenshot one element
roddy pdf [file]                        # save page as PDF
roddy download <sel> [file|-]           # download an href/src target
roddy clear-cache                       # clear the browser cache
roddy pages / page <i> / newpage [url] / closepage [i]   # tabs
roddy ax-tree [--depth N] [--json] / ax-find [--name N] [--role R] / ax-node <sel>
```

With no filename, `screenshot` writes `screenshot.png` in the **current
directory** (then `screenshot-2.png`, `screenshot-3.png`, …) and prints the
path — it does not write to stdout. Pass an explicit path when you care where it
lands.

Captures act on the browser's foreground tab. `roddy page <i>` raises the tab
it selects, so switching then capturing works. On an older binary that lacks
that fix, capturing a backgrounded tab hangs and fails with `screenshot failed:
context deadline exceeded` — the workaround is to `closepage` the other tab.

Run `roddy --help` for the complete, authoritative list if you need something
not shown here.

## Debugging recipes

These are the patterns that make roddy a real debugger, not just a clicker.

### Inspect live state

```bash
roddy text ".error-banner"                      # what does the page actually say?
roddy html "#root"                              # rendered DOM, post-hydration
roddy js "document.querySelectorAll('.row').length"   # any expression
roddy js "(function(){ return JSON.stringify(window.__APP_STATE__ || null); })()"
```

### Capture console output

`roddy logs` is the console command — it includes load-time output, because
Chrome replays its buffer to each new debugging session (see "Reading console
output" above):

```bash
roddy logs                   # everything the page has logged, including at load
roddy logs --follow          # stream live output until interrupted
```

A `js` hook is the alternative when you want **structured** output — JSON, your
own filtering, or a running tally an assertion can read back. It only catches
messages logged *after* it's installed, and it lives on `window`, so a
navigation or `reload` wipes it:

```bash
# 1) Install the hook right after opening the page
roddy js "(function(){ window.__logs=[]; ['log','warn','error','info'].forEach(function(m){ var o=console[m]; console[m]=function(){ window.__logs.push(m+': '+Array.from(arguments).join(' ')); o.apply(console,arguments); }; }); window.addEventListener('error',function(e){ window.__logs.push('uncaught: '+e.message); }); window.addEventListener('unhandledrejection',function(e){ window.__logs.push('unhandledrejection: '+e.reason); }); return 'hooked'; })()"

# 2) Reproduce the bug
roddy click "#do-the-thing"
roddy waitidle

# 3) Read what was logged
roddy js "JSON.stringify(window.__logs)"
```

The hook's output is a JSON array, so it composes with `jq` and with `roddy
assert`; `roddy logs` is the one to reach for otherwise.

### Inspect network requests

There is no dedicated network command — but the browser's Resource Timing API
already recorded every request made during load, so you can read it back
directly (this *does* capture load-time requests):

```bash
roddy js "JSON.stringify(performance.getEntriesByType('resource').map(function(e){ return {name:e.name, type:e.initiatorType, ms:Math.round(e.duration)}; }))"
```

For request/response **bodies** or headers, wrap `fetch`/`XMLHttpRequest` with a
hook (same shape as the console hook) *before* triggering the calls, then read
the captured array back.

### Screenshot to see what's actually rendered

A screenshot is often the fastest way to understand a UI bug. Save it to a file
and read it back to view it:

```bash
roddy open http://localhost:3000
roddy waitidle
roddy screenshot /tmp/repro.png      # then Read /tmp/repro.png to look at it
```

### Script a full repro / verification

Chain commands and let exit codes do the work — this is how you confirm a fix:

```bash
roddy open http://localhost:3000/login
roddy input "#email" "test@example.com"
roddy input "#password" "hunter2"
roddy click "button[type=submit]"
roddy wait ".dashboard"                 # blocks until it appears (or times out → exit 2)
roddy assert "location.pathname" "/dashboard"
roddy exists ".welcome" && echo "login flow works"
```

## Cleanup

When the debugging session is done, `roddy stop` so you don't leave a headless
Chrome lingering. If you used `--local`, the `./.roddy/` directory holds that
session's state — leave it for next time unless the user wants it gone.

## Notes

- roddy downloads and caches its own Chromium under `~/.cache/rod/` on first
  `start`. Prefer this bundled Chromium over system Chrome — some workflows
  depend on its exact build. (For reference only: `ROD_CHROME_BIN` would
  override it — but don't set it unless asked.)
- Exit codes: `0` success, `1` a check failed (`exists`/`visible`/`assert`/
  `ax-find` with no match), `2` an error (bad args, no browser, timeout). Use
  these to branch in repro scripts.
- **Roddy is a fork of Rodney**, kept alive because upstream stalled (last
  commit 2026-03-12) with several fixes unreviewed. Install or update with
  `go install github.com/goeric/roddy@latest`. Roddy's `main` is the source of
  truth — nothing needs cherry-picking.

  What Roddy has that upstream Rodney does not:

  | what it adds | upstream PR (still open) |
  |---|---|
  | `--extension` / `roddy extensions` | [#52](https://github.com/simonw/rodney/pull/52) |
  | stops the browser aborting mid-session | [#54](https://github.com/simonw/rodney/pull/54) |
  | screenshot of a non-foreground tab | [#55](https://github.com/simonw/rodney/pull/55) |

  If a machine still has the older `rodney` command, prefer `roddy`. The two keep
  separate state (`.rodney/` vs `.roddy/`), so mixing them creates a second,
  invisible session.

  Symptoms that `roddy` is actually an older build missing these fixes:
  `--extension` reports "unknown flag" (#52 missing); the browser dies mid-session
  with `failed to connect to browser (is it still running?)` (#54 missing);
  `screenshot failed: context deadline exceeded` with two tabs open (#55 missing).
