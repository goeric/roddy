# Extension-testing roadmap

Goal: make roddy the best tool for end-to-end testing of Chrome extensions,
WXT projects especially. The foundation shipped in 2026-08 (PRs #1–#4):
`roddy sw` (evaluate inside service workers, worker-aware session guards),
`roddy logs` (console replay + live follow, page and worker), stable
pre-launch extension IDs, and a CLI that reports every failure as
`error: ...` exit 2 rather than a Go panic.

The remaining items, ordered by effort. Read CLAUDE.md first — it records the
rod/Chrome facts these designs depend on, verified so they need not be
re-derived.

## 1. `roddy storage` — get/set sugar (small) — SHIPPED

Landed 2026-08: `storage get [KEY]` (whole area when KEY omitted; a missing
KEY prints `undefined`, exit 1 check-style, decided by key presence),
`storage set KEY VALUE` (VALUE is JSON when it parses, a string otherwise),
`storage rm KEY...`, `storage clear`, `--area local|sync|session|managed`
(managed read-only, surfacing Chrome's own error on writes), `--ext`/
`--timeout` as in `sw eval`. The original design notes follow.

Reading and seeding `chrome.storage` is the single most common test-setup act,
and today it requires composing JS through `sw eval`:

    roddy sw eval 'chrome.storage.local.set({user:"test"}).then(() => "ok")'

Proposed surface (settle with the maintainer before building):

    roddy storage get [KEY]            # whole area when KEY omitted
    roddy storage set KEY VALUE        # VALUE parsed as JSON, else a string
    roddy storage rm KEY
    roddy storage --area session|sync|local (default local)
    --ext ID / --timeout as in sw eval

Implementation is thin sugar over the existing `waitServiceWorker` +
`evalInServiceWorker` plumbing — build the JS, reuse the guards, format through
`printJSValue`/`formatJSValue`. Design questions to resolve: whether `set`
accepts multiple KEY=VALUE pairs; whether bare-word values are strings or an
error; whether `get` of a missing key prints `undefined` (exit 0) or exits 1
check-style. Keep the surface as small as the answers allow.

## 2. WXT ergonomics (small) — SHIPPED

Landed 2026-08 as Option B, the maintainer's pick: plain `roddy start`
auto-detects a WXT project (`wxt.config.*` in the cwd plus a built
`.output/chrome-mv3` with a manifest) and loads it with a printed notice;
`--no-extension` opts out, an explicit `--extension` suppresses detection, and
the two flags together are an error. Config without a build prints a
"run wxt build" hint on stderr and starts without an extension. When
auto-scoping would use the global session, a one-line tip suggests `--local`
(never second-guessing an explicit --local/--global or RODDY_HOME). Detection
commits to nothing it has not resolved: a build output that is present but
unloadable (mid-write, no manifest, unreadable) prints the real error as a hint
and start continues without an extension, so the auto path can never make a
plain `roddy start` fail. The stale-build warning was ruled out of scope. The
original design notes follow.

WXT builds an unpacked MV3 extension at `.output/chrome-mv3`. Today users type
`roddy start --extension .output/chrome-mv3` (documented in the README). The
open design question is how implicit to be:

- Option A: `roddy start --wxt` — explicit, zero magic, trivially documented.
- Option B: auto-detect (`wxt.config.*` present and `.output/chrome-mv3`
  exists) with a printed notice and a `--no-extension`-style opt-out.
- Option C: leave it to documentation only.

This is a taste call — brainstorm with the maintainer rather than choosing
unilaterally. Whatever lands must also consider `--local` (a WXT project wants
a project-local session) and stale builds (warn when `.output/chrome-mv3` is
older than the newest file under `src/`? possibly out of scope).

## 3. Network stubbing (large — architectural, brainstorm first)

The remaining gap against Playwright: intercepting/stubbing the requests an
extension or page makes (`Fetch.enable` + `Fetch.requestPaused` →
fulfill/fail/continue).

The hard constraint, learned from `logs`: **interception requires a live
attached session for its whole lifetime.** Every roddy invocation is a
short-lived process, so a stub registered by one command cannot survive it
unless something stays resident. Realistic architectures:

- **A. Foreground process, `--follow`-style**: `roddy stub <rules-file>` holds
  the session and applies rules until interrupted. Simple, honest, composes
  with shell (`roddy stub rules.json & ... ; kill %1`). Precedent: `logs
  --follow` already established the resident-foreground pattern, including
  target-death detection and clean exits.
- **B. Browser-lifetime helper process**: spawn a detached helper the way the
  auth proxy already works (the hidden `_proxy` subcommand, PID recorded in
  state.json, killed by `roddy stop`). Rules could then be added/removed by
  ordinary short-lived commands talking to the helper. More machinery, better
  ergonomics. Precedent exists in-repo; the failed-start cleanup paths from
  PR #4 would need extending.
- **C. Per-invocation interception**: stubs only apply during a command that
  wants them (e.g. `roddy open URL --stub rules.json` waits for load with
  rules active, then detaches). Cheapest, but stubs vanish between commands,
  which defeats most extension-testing uses.

Rules format, matching (URL pattern? method? resource type?), and whether
responses come from files or inline JSON are all open. Do NOT start this one
without a design round with the maintainer — it is the only item with real
architectural risk. When the session-holding piece is built, reuse the
`openLogStream` lessons wholesale: dedicated flat session, subscribe before
enable, close-on-target-death, bounded setup calls, `-race` the handoff.

## Process notes for the orchestrator

- Each item: brainstorm the surface with the maintainer (AskUserQuestion for
  genuine forks), spike anything unproven, TDD, branch → PR → review toolkit →
  fix round → verification pass → simplifier → merge on the maintainer's word.
  He gates merges personally; do not merge unprompted.
- Skill and README updates ship in the same PR as the feature; a SKILL.md
  change requires a `.claude-plugin/plugin.json` version bump to propagate.
- The test fixtures for extension/SW work already exist in sw_test.go
  (`writeSWTestExtension`, `launchWithSWExtension`, `launchWithExtensions`) and
  main_test.go (`/logs-page`, `/sw-page` routes); extend rather than duplicate.
- Storage sugar (#1) and WXT (#2) are each one-sitting items; stubbing (#3) is
  a multi-round feature on the scale of `logs`.
