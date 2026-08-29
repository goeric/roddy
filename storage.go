package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ysmood/gson"
)

const storageUsage = "usage: roddy storage <get [KEY] | set KEY VALUE | rm KEY... | clear>\n" +
	"       [--area local|sync|session|managed] [--ext ID] [--timeout DUR]"

var storageAreas = map[string]bool{"local": true, "sync": true, "session": true, "managed": true}

// storageOp is one parsed storage invocation.
type storageOp struct {
	verb  string   // get, set, rm or clear
	key   string   // get (empty means the whole area) and set
	value string   // set, exactly as typed
	keys  []string // rm
}

// parseStorageFlags pulls --area, --ext and --timeout out of args wherever they
// appear, the way parseSWFlags does. Unlike sw eval there is no expression to
// keep intact, so an unrecognized flag is an error rather than data — except a
// negative number ("-1.5"), which is a value. "--" ends the flags for keys or
// values that really do start with a dash.
func parseStorageFlags(args []string) (area, ext string, timeout time.Duration, rest []string, err error) {
	area = "local"
	timeout = 5 * time.Second
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return area, ext, timeout, append(rest, args[i+1:]...), nil
		}
		name, value, inline := strings.Cut(args[i], "=")
		switch name {
		case "--area", "-area", "--ext", "-ext", "--timeout", "-timeout":
		default:
			if storageFlagLike(name) {
				return "", "", 0, nil, fmt.Errorf(`unknown flag: %s (use "--" before keys or values that start with a dash)`, name)
			}
			rest = append(rest, args[i])
			continue
		}
		if value, i, err = takeFlagValue(args, i, name, value, inline); err != nil {
			return "", "", 0, nil, err
		}
		switch name {
		case "--area", "-area":
			// The empty string an unset shell variable expands to is caught here
			// too, rather than quietly meaning local.
			if !storageAreas[value] {
				return "", "", 0, nil, fmt.Errorf("invalid value %q for flag %s (areas: local, sync, session, managed)", value, name)
			}
			area = value
		case "--ext", "-ext":
			if value == "" {
				return "", "", 0, nil, fmt.Errorf("flag %s needs a non-empty value", name)
			}
			// Extension IDs are letters a-p, so a flag-like value is the next flag
			// swallowed by a missing ID rather than an ID.
			if storageFlagLike(value) {
				return "", "", 0, nil, fmt.Errorf("invalid value %q for flag %s (extension IDs are letters a-p)", value, name)
			}
			ext = value
		default: // --timeout, -timeout
			if timeout, err = time.ParseDuration(value); err != nil {
				return "", "", 0, nil, fmt.Errorf("invalid value %q for flag %s: %w", value, name, err)
			}
		}
	}
	return area, ext, timeout, rest, nil
}

// storageFlagLike reports whether a bare argument is a mistyped flag rather
// than data: a dash whose next character is neither a digit nor a dot, so
// "-1.5" and "-.5" are data and "-1x" passes as data too.
func storageFlagLike(s string) bool {
	return len(s) >= 2 && s[0] == '-' && s[1] != '.' && (s[1] < '0' || s[1] > '9')
}

// storageArgs validates the positional arguments once the flags are gone. An
// explicitly empty KEY is rejected everywhere it can appear: it is what an
// unset shell variable expands to, and for get it would otherwise dump the
// whole area and exit 0, making "get X || set X" always pass.
func storageArgs(rest []string) (storageOp, error) {
	if len(rest) == 0 {
		return storageOp{}, fmt.Errorf("missing storage subcommand")
	}
	op := storageOp{verb: rest[0]}
	args := rest[1:]
	switch op.verb {
	case "get":
		if len(args) > 1 {
			return storageOp{}, fmt.Errorf("get takes at most one KEY")
		}
		if len(args) == 1 {
			if args[0] == "" {
				return storageOp{}, fmt.Errorf("get needs a non-empty KEY (omit it for the whole area)")
			}
			op.key = args[0]
		}
	case "set":
		if len(args) != 2 {
			return storageOp{}, fmt.Errorf("set takes exactly KEY VALUE")
		}
		if args[0] == "" {
			return storageOp{}, fmt.Errorf("set needs a non-empty KEY")
		}
		op.key, op.value = args[0], args[1]
	case "rm":
		if len(args) == 0 {
			return storageOp{}, fmt.Errorf("rm needs at least one KEY")
		}
		for _, k := range args {
			if k == "" {
				return storageOp{}, fmt.Errorf("rm needs a non-empty KEY")
			}
		}
		op.keys = args
	case "clear":
		if len(args) != 0 {
			return storageOp{}, fmt.Errorf("clear takes no arguments")
		}
	default:
		return storageOp{}, fmt.Errorf("unknown storage subcommand: %q", op.verb)
	}
	return op, nil
}

// storageValueJS turns a command-line VALUE into a JS literal: valid JSON is
// embedded as is, so 3, true and {"a":1} keep their types; anything else is a
// string. A string that would otherwise parse is forced by quoting it on the
// command line: '"true"'.
func storageValueJS(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	return jsStringLit(s)
}

// jsStringLit renders s as a JS string literal via its JSON encoding, which
// cannot fail for a string.
func jsStringLit(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func storageGetAllJS(area string) string {
	return fmt.Sprintf("chrome.storage.%s.get(null)", area)
}

// storageGetJS resolves to a [present, value] pair: presence keeps a stored
// null apart from a missing key, so it can drive the exit code while the value
// drives the output.
func storageGetJS(area, key string) string {
	k := jsStringLit(key)
	return fmt.Sprintf("chrome.storage.%s.get([%s]).then(r => [Object.hasOwn(r, %s), r[%s]])", area, k, k, k)
}

// storageSetJS hands Chrome a JSON.parse of the payload rather than an object
// literal: a non-computed "__proto__" key in a literal sets the prototype and
// creates no own property (ES B.3.1), so a literal would silently drop such a
// key — and any nested one inside VALUE — while still reporting success.
// storageValueJS always returns JSON text, so the payload is valid JSON.
func storageSetJS(area, key, valueJS string) string {
	payload := "{" + jsStringLit(key) + ":" + valueJS + "}"
	return fmt.Sprintf("chrome.storage.%s.set(JSON.parse(%s))", area, jsStringLit(payload))
}

func storageRemoveJS(area string, keys []string) string {
	b, _ := json.Marshal(keys)
	return fmt.Sprintf("chrome.storage.%s.remove(%s)", area, b)
}

func storageClearJS(area string) string {
	return fmt.Sprintf("chrome.storage.%s.clear()", area)
}

// storageGetResult unpacks what storageGetJS resolved to. Anything but the
// constructed pair means an invariant of our own generated JS broke, which
// reports as an error rather than "not present": the documented
// "get X || set X" idiom would turn a wrong absence into an overwrite.
func storageGetResult(v gson.JSON) (present bool, value gson.JSON, err error) {
	pair := v.Arr()
	if len(pair) != 2 {
		return false, gson.New(nil), fmt.Errorf("unexpected storage get result: %s", v.JSON("", ""))
	}
	return pair[0].Bool(), pair[1], nil
}

// storageGuardJS names the missing permission instead of letting the read of
// chrome.storage.<area> fail as "TypeError: Cannot read properties of
// undefined (reading 'local')". The rejection keeps the whole thing an
// expression, which is what evalInServiceWorker's IIFE returns and awaits.
func storageGuardJS(expr string) string {
	const reason = `chrome.storage is unavailable: the extension must declare the "storage" permission in its manifest`
	return fmt.Sprintf("(globalThis.chrome?.storage ? (%s) : Promise.reject(Error(%s)))", expr, jsStringLit(reason))
}

// cmdStorage handles "roddy storage get/set/rm/clear": thin sugar over the
// same worker plumbing as sw eval, so every session guard there applies here.
func cmdStorage(args []string) {
	area, ext, timeout, rest, err := parseStorageFlags(args)
	if err != nil {
		fatal("%v\n%s", err, storageUsage)
	}
	op, err := storageArgs(rest)
	if err != nil {
		fatal("%v\n%s", err, storageUsage)
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	if err := checkExtensionFilter(s.Extensions, ext, true); err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	sw, err := waitServiceWorker(browser, ext, timeout)
	if err != nil {
		fatal("%v", err)
	}
	eval := func(js string) gson.JSON {
		v, err := evalInServiceWorker(browser, sw, storageGuardJS(js))
		if err != nil {
			fatal("%v", err)
		}
		return v
	}

	switch op.verb {
	case "get":
		if op.key == "" {
			fmt.Println(formatJSValue(eval(storageGetAllJS(area))))
			return
		}
		present, value, err := storageGetResult(eval(storageGetJS(area, op.key)))
		if err != nil {
			fatal("%v", err)
		}
		if !present {
			fmt.Println("undefined")
			os.Exit(1)
		}
		fmt.Println(formatJSValue(value))
	case "set":
		eval(storageSetJS(area, op.key, storageValueJS(op.value)))
		fmt.Println("Set")
	case "rm":
		eval(storageRemoveJS(area, op.keys))
		fmt.Println("Removed")
	case "clear":
		eval(storageClearJS(area))
		fmt.Println("Cleared")
	}
}
