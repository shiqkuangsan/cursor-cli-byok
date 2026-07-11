package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestServeBackgroundChildRequiresInheritedLock(t *testing.T) {
	var stderr bytes.Buffer
	app := App{
		Context: context.Background(),
		Stderr:  &stderr,
		Getenv: commandEnv(map[string]string{
			"HOME": t.TempDir(),
		}),
	}

	exitCode := app.Run([]string{"serve", "--background-child"})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want missing inherited lock failure")
	}
	if !strings.Contains(stderr.String(), "inherited") || !strings.Contains(stderr.String(), "lock") {
		t.Fatalf("stderr = %q, want inherited lock diagnostic", stderr.String())
	}
}
