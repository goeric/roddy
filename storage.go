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
// negative number ("-1.5"), which is a value. "--" ends the flags for keys that
// really do start with a dash.
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
				return "", "", 0, nil, fmt.Errorf("unknown flag: %s", name)
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
// than data: it starts with a dash and is not a negative number.
func storageFlagLike(s string) bool {
	return len(s) >= 2 && s[0] == '-' && s[1] != '.' && (s[1] < '0' || s[1] > '9')
}

// storageArgs validates the positional arguments once the flags are gone.
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
			op.key = args[0]
		}
	case "set":
		if len(args) != 2 {
			return storageOp{}, fmt.Errorf("set takes exactly KEY VALUE")
		}
		op.key, op.value = args[0], args[1]
	case "rm":
		if len(args) == 0 {
			return storageOp{}, fmt.Errorf("rm needs at least one KEY")
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

func storageSetJS(area, key, valueJS string) string {
	return fmt.Sprintf("chrome.storage.%s.set({%s: %s})", area, jsStringLit(key), valueJS)
}

func storageRemoveJS(area string, keys []string) string {
	b, _ := json.Marshal(keys)
	return fmt.Sprintf("chrome.storage.%s.remove(%s)", area, b)
}

func storageClearJS(area string) string {
	return fmt.Sprintf("chrome.storage.%s.clear()", area)
}

// storageGetResult unpacks what storageGetJS resolved to. Anything but the
// constructed pair cannot happen short of the eval machinery failing, which
// already errored; the guard only keeps a surprise from becoming a panic.
func storageGetResult(v gson.JSON) (present bool, value gson.JSON) {
	pair := v.Arr()
	if len(pair) != 2 {
		return false, gson.New(nil)
	}
	return pair[0].Bool(), pair[1]
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
		v, err := evalInServiceWorker(browser, sw, js)
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
		present, value := storageGetResult(eval(storageGetJS(area, op.key)))
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
