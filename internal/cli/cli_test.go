package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVersion(t *testing.T) {
	code, stdout, _ := run(t, "version")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasPrefix(stdout, "pear ") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, stderr := run(t, "wat")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestValidate(t *testing.T) {
	path := writeConfig(t, "config_version = 1\n[dock]\nautohide = true\n")
	code, stdout, stderr := run(t, "validate", "--config", path)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "1 setting(s)") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestValidateUnknownKey(t *testing.T) {
	path := writeConfig(t, "config_version = 1\n[dock]\nautohid = true\n")
	code, _, stderr := run(t, "validate", "--config", path)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "did you mean") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestList(t *testing.T) {
	code, stdout, _ := run(t, "list")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "dock.autohide") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestListScope(t *testing.T) {
	code, stdout, _ := run(t, "list", "--scope", "user")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(stdout, "system") {
		t.Fatalf("unexpected system setting in output:\n%s", stdout)
	}
}

func TestExplainUnknown(t *testing.T) {
	code, _, stderr := run(t, "explain", "dock.autohid")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "dock.autohide") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestApplyRejectsExtraArgs(t *testing.T) {
	code, _, stderr := run(t, "apply", "extra")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unexpected argument") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConfigureRequiresTTY(t *testing.T) {
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		t.Skip("running in an interactive terminal")
	}
	code, _, stderr := run(t, "configure")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "interactive terminal") {
		t.Fatalf("stderr = %q", stderr)
	}
}
