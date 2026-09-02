# roddy — development guide

Go CLI driving headless Chrome via go-rod. Single package `main`, module
`github.com/goeric/roddy`. Fork of simonw/rodney (stalled upstream; PRs #52/54/55
there stay open, their head branches live on the parked `goeric/rodney` fork —
do not delete that repo, it would close them).

## Conventions

- CLI errors go through `fatal()` (prints `error: ...`, exit 2). Exit 1 is
  reserved for check-style commands whose check failed (`exists`, `visible`,
  `assert`, `ax-find`, empty `sw list`). Never let a rod `Must*` panic reach the
  user — every one was converted in PR #3; keep it that way. (`Browser.MustClose`
  is the one non-panicking Must — it's `_ = Close()` in rod.)
- Commands with expression arguments (`js`, `sw eval`) don't use flag.FlagSet —
  it chokes on `-1 + 2`. Pre-extract flags from anywhere in args instead
  (`parseSWFlags`, `parseLogsFlags`, shared `takeFlagValue`); bool flags reject
  inline values (`--follow=false` is an error, not false).
- Comments state non-obvious constraints only, tersely, with error strings
  quoted verbatim. Do not narrate.
- Tests: browser-backed, sequential. Shared `env.browser` (TestMain) for page
  work — it runs `--single-process`, which breaks extensions and service
  workers, so extension/SW tests launch per-test browsers via
  `configureExtensions(baseLauncher()...)`. cmd* functions call os.Exit and are
  untestable by convention — put logic in extracted helpers and test those.
  `go test ./...` takes ~35s and launches real Chromium.
- Repo is gofmt-clean; keep it so. `go vet` must pass.

## Hard-won facts (verified empirically — do not re-derive, do not "fix")

rod v0.116.2:
- `PageFromSession` builds a Page with a nil internal lock: `Page.Timeout` on it
  PANICS. Put deadlines on a Browser clone: `browser.Timeout(d).PageFromSession(...)`.
- `Browser.Pages()` enables only the Page domain on its sessions, never Runtime.
  Page ordering is newest-first and stable minus closed pages.
- `EachEvent` subscribes at call time (only the returned wait() blocks); its
  callbacks run synchronously inside wait(), so `wait(); close(ch)` in one
  goroutine guarantees no send-after-close. goob's event pipe is unbounded —
  a slow consumer never stalls rod's CDP reader.
- `Page.Eval` CALLS a function literal; raw `Runtime.evaluate` does not — wrap
  expressions in an IIFE `(() => { return (expr); })()` or `{a: 1}` parses as a
  labelled block.
- A `*rod.NavigationError` already reads `navigation failed: net::ERR_…`, so
  `navigationFailure`'s own prefix doubles it in the CLI output — pre-fork
  behaviour, kept so callers and docs still match `navigation failed: `.

Chrome (pinned Chromium 128 via rod's cache):
- Unpacked-extension IDs are a hash of the absolute path (a-p alphabet), so IDs
  are stable and known before launch — `extensionID()` reproduces Chrome's
  computation. The path literal in TestExtensionID_MatchesChromeAlgorithm keeps
  its pre-fork "rodney" spelling on purpose: it's a golden value captured from
  real Chrome.
- Extensions need new headless (`--headless=new`) and break under
  `--single-process`; `configureExtensions` handles both.
- MV3 service workers: attach with `TargetAttachToTarget{Flatten: true}` (the
  Page abstraction is rejected: "Operation is only supported for pages, not
  workers"). They suspend after ~30s idle; opening an extension page does NOT
  wake one — dispatching an event (e.g. `chrome.runtime.sendMessage`) does.
  A restarted worker is a new target with an empty console buffer.
- Console replay: Chrome replays buffered console/exception/Log events on each
  disable→enable TRANSITION of a session (not "once per target") — subscribe
  before enabling, on a dedicated session where nothing has enabled Runtime yet.
  Replayed object args carry no preview (they print as `Object`); live ones
  carry a one-level preview. The Log domain IS supported on worker targets.
- `ReturnByValue` results for NaN/Infinity/-0 arrive in `UnserializableValue`
  with `Value` empty — read it (`remoteObjectValue`) or they print as null.
- Browser-level `Fetch.enable` (`urlPattern "*"`, Request stage) intercepts
  pages and content scripts; the interception itself bypasses the HTTP cache;
  redirect hops re-pause carrying `RedirectedRequestID` on the pause event (no
  Network domain needed). It applies per COMMITTED DOCUMENT: a page whose
  document committed before the enable keeps the live network forever — no
  pause events at all — until it is re-navigated.
- An MV3 worker's ORGANIC fetches (its own extension logic) NEVER pause under
  the browser-level enable — only fetches issued through a debugger eval do,
  which is how an eval-only spike wrongly "verified" full SW coverage. Worse:
  the worker's request path bakes interception in at WORKER START, so enabling
  Fetch on an already-running worker's session also changes nothing organic
  (fresh-profile installs restart the worker soon after launch, masking this;
  reused profiles reproduce it deterministically). The working model is
  Playwright's: Target.setAutoAttach with WaitForDebuggerOnStart+Flatten,
  enable Fetch on the attached session, then Runtime.runIfWaitingForDebugger —
  the worker's first instruction runs intercepted. Workers already running
  must be restarted (TargetCloseTarget; the registration survives — the
  ServiceWorker domain is rejected on the browser session). Worker pauses can
  arrive tagged with the worker session's ID — answer on the arriving session.
  Auto-attached targets left waiting hang the whole browser's new tabs:
  always resume them. CAVEAT that hid all of this for a while: with the dev
  snapshot's field-trial config ACTIVE the browser routes organic worker
  fetches through browser-level interception after all — so a test browser
  missing configureExperiments (as baseLauncher was) passes tests the shipped
  configuration fails.
- rod's `Browser.Connect` ends with `SetDiscoverTargets{Discover: true}`, so
  the TargetCreated replay of existing targets fires before any EachEvent
  subscription exists and a later re-call is a no-transition no-op that
  replays nothing. Enumerate existing targets with `TargetGetTargets`;
  TargetCreated is only good for targets that appear after subscribing.
- `FetchFulfillRequest.Body` has no omitempty: nil marshals as JSON null and
  Chrome rejects the fulfill with "binary value expected" — always send at
  least `[]byte{}`.
- rod `EachEvent` never filters delivery by callback arity: `eachEvent` keeps a
  message when `sessionID == "" || msg.SessionID == sessionID`, and
  `Browser.EachEvent` passes "" — a browser-level subscription sees every
  session's events either way. The optional `proto.TargetSessionID` parameter
  only reports which session the event arrived on, which is what stub.go
  answers a pause back on (every pause observed here carries "").
- Attaching a flat session to a worker WHILE its own intercepted request is
  in flight can wedge the next eval-launched fetch: the pause never surfaces
  on any session while page interception stays healthy, and the eval times
  out (retrying works). Reproduced 9-in-10 in an E2E that attached mid-
  traffic vs 10/10 once the worker's traffic settled first; a warm-up
  attach/detach eval helps but does not suffice on its own under the shipped
  flags. Practical shape: `sw eval 'fetch(...)'` under an active stub can
  rarely time out if it races the extension's own stubbed traffic.

## Workflow (how this repo's features have shipped)

Feature work goes on a branch → PR → the pr-review-toolkit agents → a fix round
addressing findings → a verification pass confirming each finding
FIXED/PARTIAL/NOT-FIXED → code-simplifier as final gate → CI green → merge only
on the maintainer's explicit word. Spike unproven mechanisms first (throwaway
code in the scratchpad); TDD the real implementation (failing test first).
Review agents' claims get checked against source before acting — agents have
both found real bugs and asserted false premises. Never edit files while review
agents are running on them.

After merging anything that changes `skills/roddy/SKILL.md`: bump the version in
`.claude-plugin/plugin.json` or installed plugins will not pick it up, then
`claude plugin marketplace update roddy && claude plugin update roddy`. Rebuild
`~/.local/bin/roddy` from main after every merge.

## Roadmap

See `docs/extension-testing-roadmap.md` for the planned work (storage sugar,
WXT ergonomics, network stubbing) with design constraints already established.
