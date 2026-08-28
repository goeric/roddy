# roddy assert: asserting JavaScript expressions

*2026-02-17T21:46:40Z by Showboat 0.6.0*
<!-- showboat-id: 6dd55f0e-350e-4323-9650-92bdf2739734 -->

The new `roddy assert` command lets you assert JavaScript expressions directly from the command line. It supports two modes: **truthy mode** (one argument) checks that an expression evaluates to a truthy value, and **equality mode** (two arguments) checks that the result matches an expected string. Both modes use exit code 0 for pass and exit code 1 for failure, consistent with the other check commands (`exists`, `visible`, `ax-find`).

We will test against a small Starlette app (`demo_app.py` in this directory) that serves a task tracker page with a title, heading, task list, and login indicator. Start it with `uv run demo_app.py`.

Start the browser and open the demo app.

```bash
./roddy start 2>/dev/null | grep -v "^Auth proxy\|^Debug URL" && ./roddy open http://127.0.0.1:18092/
```

```output
Chrome started (PID 18362)
Task Tracker
```

## Truthy mode (one argument)

With a single argument, `roddy assert` evaluates the JavaScript expression and exits 0 if the result is truthy, or exits 1 if falsy (false, 0, null, undefined, or empty string).

Check that a `.logged-in` element exists on the page:

```bash
./roddy assert "document.querySelector('.logged-in')" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

A boolean expression evaluates to `true` which is truthy:

```bash
./roddy assert "document.title === 'Task Tracker'" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

When the expression evaluates to a falsy value — `null`, `false`, `0`, `undefined`, or an empty string — the command prints a failure message and exits 1.

Query for a nonexistent element (returns `null`):

```bash
./roddy assert "document.querySelector('.nonexistent')"; echo "exit code: $?"
```

```output
fail: got null
exit code: 1
```

A boolean `false`:

```bash
./roddy assert "1 === 2"; echo "exit code: $?"
```

```output
fail: got false
exit code: 1
```

## Equality mode (two arguments)

With two arguments, `roddy assert` evaluates the expression and compares its string representation to the expected value. The formatting matches what `roddy js` would output — strings are unquoted, numbers are plain digits.

Check the page title:

```bash
./roddy assert "document.title" "Task Tracker" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

Count the number of tasks:

```bash
./roddy assert "document.querySelectorAll('.task').length" "3" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

Check the text content of the heading:

```bash
./roddy assert "document.querySelector('h1').textContent" "Task Tracker" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

Read a data attribute:

```bash
./roddy assert "document.querySelector('.logged-in').dataset.user" "alice" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

When the values do not match, the failure message shows both the actual and expected values for easy debugging:

```bash
./roddy assert "document.title" "Wrong Title"; echo "exit code: $?"
```

```output
fail: got "Task Tracker", expected "Wrong Title"
exit code: 1
```

```bash
./roddy assert "document.querySelectorAll('.task').length" "5"; echo "exit code: $?"
```

```output
fail: got "3", expected "5"
exit code: 1
```

## Asserting across pages

Navigate to the About page and assert its title, then navigate back and re-check.

```bash
./roddy open http://127.0.0.1:18092/about && ./roddy assert "document.title" "About - Task Tracker"
```

```output
About - Task Tracker
pass
```

```bash
./roddy back && ./roddy assert "document.title" "Task Tracker"
```

```output
http://127.0.0.1:18092/
pass
```

## Custom failure messages with --message

The `--message` (or `-m`) flag adds a human-readable label to the failure output. The diagnostic details (actual vs expected) are still included in parentheses.

```bash
./roddy assert "document.querySelector('.nonexistent')" -m "User should be logged in"; echo "exit code: $?"
```

```output
fail: User should be logged in (got null)
exit code: 1
```

```bash
./roddy assert "document.title" "Dashboard" --message "Wrong page loaded"; echo "exit code: $?"
```

```output
fail: Wrong page loaded (got "Task Tracker", expected "Dashboard")
exit code: 1
```

When the assertion passes, the message is not shown — only "pass" is printed:

```bash
./roddy assert "document.title" "Task Tracker" -m "Wrong page" && echo "exit code: $?"
```

```output
pass
exit code: 0
```

## Combining assert with other check commands

The `assert` command uses exit code 1 for failures, just like `exists`, `visible`, and `ax-find`. This means it works naturally with the `check` helper pattern for running multiple assertions without aborting on the first failure. Adding `-m` makes the output self-documenting.

```bash
FAIL=0
check() {
    "$@" 2>/dev/null || { FAIL=1; }
}

# These pass
check ./roddy exists "h1"
check ./roddy visible "h1"
check ./roddy assert "document.title" "Task Tracker" -m "Title should be Task Tracker"
check ./roddy assert "document.querySelectorAll('.task').length" "3" -m "Should have 3 tasks"
check ./roddy assert "document.querySelector('.logged-in').dataset.user" "alice" -m "Should be logged in as alice"

# These fail
check ./roddy assert "document.title" "Wrong Title" -m "Wrong page loaded"
check ./roddy assert "document.querySelectorAll('.task').length" "10" -m "Expected 10 tasks"
check ./roddy assert "document.querySelector('.nonexistent')" -m "Missing element"

if [ "$FAIL" -ne 0 ]; then
    echo "---"
    echo "Some checks failed"
else
    echo "All checks passed"
fi

```

```output
true
true
pass
pass
pass
fail: Wrong page loaded (got "Task Tracker", expected "Wrong Title")
fail: Expected 10 tasks (got "3", expected "10")
fail: Missing element (got null)
---
Some checks failed
```

The five passing checks ran silently, while the three failing checks printed self-describing messages with diagnostic details. The `--message` flag makes it immediately clear *what* failed without having to decode the raw JS expression.

Stop the browser.

```bash
./roddy stop
```

```output
Chrome stopped
```
