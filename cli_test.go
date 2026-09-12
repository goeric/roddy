package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The test binary dispatches the real CLI before TestMain launches a browser.
func runCLI(t *testing.T, wantExit int, args ...string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "RODDY_TEST_CLI=1", "ROD_TIMEOUT=5")
	output, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%q: %v\n%s", args, err, output)
		}
		code = exit.ExitCode()
	}
	if code != wantExit {
		t.Fatalf("%q: exit %d, want %d\n%s", args, code, wantExit, output)
	}
	return string(output)
}

func TestViewportCLI(t *testing.T) {
	_, s := retireFixture(t)
	mustSaveState(t, s)
	runCLI(t, 0, "viewport", "320", "568")
	runCLI(t, 0, "open", env.server.URL)
	runCLI(t, 0, "assert", "innerWidth", "320")
	runCLI(t, 0, "js", `(() => { document.body.innerHTML='<div style="width:400px;height:100px">overflow</div>'; return true; })()`)
	runCLI(t, 0, "screenshot", "-w", "375", "-h", "667", filepath.Join(t.TempDir(), "temporary.png"))
	runCLI(t, 0, "assert", "innerWidth", "320")
	runCLI(t, 0, "assert", "document.documentElement.scrollWidth > innerWidth")
	runCLI(t, 0, "reload")
	runCLI(t, 0, "assert", "innerWidth", "320")
	runCLI(t, 0, "newpage", env.server.URL)
	runCLI(t, 0, "assert", "innerWidth", "320")
	runCLI(t, 2, "viewport", "0", "568")
	runCLI(t, 0, "assert", "innerWidth", "320")
	runCLI(t, 0, "viewport", "reset")
	runCLI(t, 0, "assert", "innerWidth", "1280")
	state, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Viewport != nil {
		t.Fatal("reset did not clear the saved viewport")
	}
}
