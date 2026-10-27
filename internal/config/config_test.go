package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metru/pear/internal/catalog"
)

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return cat
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustResolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := ResolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `
config_version = 1

[dock]
autohide = true
tilesize = 44
autohide_delay = 0.25
position = "left"

[screenshots]
location = "~/Pictures/Screenshots"
`)
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Fatalf("Version = %d", cfg.Version)
	}
	if got := cfg.Values["dock.autohide"]; got != true {
		t.Fatalf("dock.autohide = %#v", got)
	}
	if got := cfg.Values["dock.tilesize"]; got != int64(44) {
		t.Fatalf("dock.tilesize = %#v", got)
	}
	if got := cfg.Values["dock.autohide_delay"]; got != 0.25 {
		t.Fatalf("dock.autohide_delay = %#v", got)
	}
	if got := cfg.Values["dock.position"]; got != "left" {
		t.Fatalf("dock.position = %#v", got)
	}
	home, _ := os.UserHomeDir()
	if got := cfg.Values["screenshots.location"]; got != filepath.Join(home, "Pictures/Screenshots") {
		t.Fatalf("screenshots.location = %#v", got)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "missing version",
			content: "[dock]\nautohide = true\n",
			want:    "missing required config_version",
		},
		{
			name:    "wrong version",
			content: "config_version = 2\n",
			want:    "config_version must be 1",
		},
		{
			name:    "unknown key",
			content: "config_version = 1\n[dock]\nautohid = true\n",
			want:    `unknown setting "dock.autohid"`,
		},
		{
			name:    "wrong type",
			content: "config_version = 1\n[dock]\nautohide = \"yes\"\n",
			want:    "expected boolean",
		},
		{
			name:    "invalid enum",
			content: "config_version = 1\n[dock]\nminimize_effect = \"explode\"\n",
			want:    "invalid value",
		},
		{
			name:    "relative path",
			content: "config_version = 1\n[screenshots]\nlocation = \"relative\"\n",
			want:    "path must be absolute",
		},
		{
			name:    "unknown reset",
			content: "config_version = 1\nreset = [\"dock.nope\"]\n",
			want:    "unknown setting in reset",
		},
		{
			name:    "set and reset",
			content: "config_version = 1\nreset = [\"dock.tilesize\"]\n[dock]\ntilesize = 44\n",
			want:    "both set and reset",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeFile(t, dir, "config.toml", test.content)
			_, err := Load(path, testCatalog(t))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not contain %q", err, test.want)
			}
		})
	}
}

func TestIncludeOverrides(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "base.toml", `
config_version = 1
[dock]
tilesize = 40
autohide = true
`)
	path := writeFile(t, dir, "config.toml", `
config_version = 1
include = ["base.toml"]
[dock]
tilesize = 44
`)
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, _ := ResolvePath(path)
	resolvedBase, _ := ResolvePath(base)
	if got := cfg.Values["dock.tilesize"]; got != int64(44) {
		t.Fatalf("dock.tilesize = %#v, want 44", got)
	}
	if got := cfg.Values["dock.autohide"]; got != true {
		t.Fatalf("dock.autohide = %#v, want true", got)
	}
	if got := cfg.Origins["dock.tilesize"]; got != resolvedPath {
		t.Fatalf("dock.tilesize origin = %q, want %q", got, resolvedPath)
	}
	if got := cfg.Origins["dock.autohide"]; got != resolvedBase {
		t.Fatalf("dock.autohide origin = %q, want %q", got, resolvedBase)
	}
	if got := cfg.Inherited.Values["dock.tilesize"]; got != int64(40) {
		t.Fatalf("inherited tilesize = %#v, want 40", got)
	}
}

func TestLaterResetOverridesIncludedValue(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "base.toml", `
[dock]
autohide = true
`)
	path := writeFile(t, dir, "config.toml", `
config_version = 1
include = ["base.toml"]
reset = ["dock.autohide"]
`)
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Values["dock.autohide"]; ok {
		t.Fatal("included value survived the root reset")
	}
	if len(cfg.Resets) != 1 || cfg.Resets[0] != "dock.autohide" {
		t.Fatalf("resets = %v, want [dock.autohide]", cfg.Resets)
	}
	if cfg.ResetOrigins["dock.autohide"] != mustResolve(t, path) {
		t.Fatalf("reset origin = %q", cfg.ResetOrigins["dock.autohide"])
	}
	if got := cfg.Inherited.Values["dock.autohide"]; got != true {
		t.Fatalf("inherited value = %#v, want true", got)
	}
}

func TestLaterValueOverridesIncludedReset(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "base.toml", `
reset = ["dock.autohide"]
`)
	path := writeFile(t, dir, "config.toml", `
config_version = 1
include = ["base.toml"]
[dock]
autohide = true
`)
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Values["dock.autohide"]; got != true {
		t.Fatalf("dock.autohide = %#v, want true", got)
	}
	if cfg.Inherited.HasReset("dock.autohide") != true {
		t.Fatal("inherited reset missing")
	}
	if len(cfg.Resets) != 0 {
		t.Fatalf("resets = %v, want none", cfg.Resets)
	}
}

// StateHasReset is a tiny test helper to keep assertions readable.
func (c *Config) StateHasReset() bool {
	return len(c.Resets) == 1 && c.Resets[0] == "dock.autohide"
}

func TestIncludeThroughSymlink(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "config.toml", `
config_version = 1
include = ["base.toml"]
`)
	writeFile(t, repo, "base.toml", `
[dock]
autohide = true
`)
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "config.toml")
	if err := os.Symlink(filepath.Join(repo, "config.toml"), link); err != nil {
		t.Skipf("symlink: %v", err)
	}

	cfg, err := Load(link, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Values["dock.autohide"]; got != true {
		t.Fatalf("dock.autohide = %#v, want true", got)
	}
}

func TestIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.toml", "config_version = 1\ninclude = [\"b.toml\"]\n")
	b := writeFile(t, dir, "b.toml", "include = [\"a.toml\"]\n")
	_, err := Load(b, testCatalog(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "included more than once") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResets(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `
config_version = 1
reset = ["dock.tilesize", "dock.autohide"]
[dock]
position = "left"
`)
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dock.autohide", "dock.tilesize"}
	if len(cfg.Resets) != len(want) {
		t.Fatalf("Resets = %v, want %v", cfg.Resets, want)
	}
	for i := range want {
		if cfg.Resets[i] != want[i] {
			t.Fatalf("Resets = %v, want %v", cfg.Resets, want)
		}
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"), testCatalog(t))
	if err == nil || !strings.Contains(err.Error(), "config not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultPathFromEnv(t *testing.T) {
	t.Setenv("PEAR_CONFIG", "~/dotfiles/pear.toml")
	home, _ := os.UserHomeDir()
	if got := DefaultPath(); got != filepath.Join(home, "dotfiles/pear.toml") {
		t.Fatalf("DefaultPath = %q", got)
	}

	t.Setenv("PEAR_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := DefaultPath(); got != filepath.Join("/tmp/xdg", "pear", "config.toml") {
		t.Fatalf("DefaultPath = %q", got)
	}
}
