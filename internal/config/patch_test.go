package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apply(t *testing.T, path string, edits ...Edit) error {
	t.Helper()
	return ApplyEdits(path, testCatalog(t), edits)
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPatchReplacePreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

# Dock settings
[dock]
# whether to hide
autohide = false  # keep this
tilesize = 44
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	want := `config_version = 1

# Dock settings
[dock]
# whether to hide
autohide = true  # keep this
tilesize = 44
`
	if got != want {
		t.Fatalf("patch result:\n%s\nwant:\n%s", got, want)
	}
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Values["dock.autohide"] != true {
		t.Fatalf("value = %#v", cfg.Values["dock.autohide"])
	}
}

func TestPatchInsertMissingKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

[dock]
tilesize = 44
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "tilesize = 44\nautohide = true\n") {
		t.Fatalf("unexpected result:\n%s", got)
	}
}

func TestPatchInsertMissingTable(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", "config_version = 1\n")
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "[dock]\nautohide = true") {
		t.Fatalf("unexpected result:\n%s", got)
	}
	if _, err := Load(path, testCatalog(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPatchRemoveValue(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

[dock]
autohide = true
tilesize = 44
`)
	if err := apply(t, path, Edit{Kind: RemoveValue, ID: "dock.tilesize"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "tilesize") {
		t.Fatalf("tilesize not removed:\n%s", got)
	}
	if !strings.Contains(got, "autohide = true") {
		t.Fatalf("autohide lost:\n%s", got)
	}
}

func TestPatchMultiLineValue(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

[dock]
autohide = [
  false,
  # a comment inside the array
]
tilesize = 44
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "autohide = true\ntilesize = 44") {
		t.Fatalf("unexpected result:\n%s", got)
	}
	if _, err := Load(path, testCatalog(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPatchStringWithHash(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

[screenshots]
location = "~/Pictures" # default place
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "screenshots.location", Value: "/Users/me/#1"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, `location = "/Users/me/#1"  # default place`) {
		t.Fatalf("unexpected result:\n%s", got)
	}
	cfg, err := Load(path, testCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Values["screenshots.location"] != "/Users/me/#1" {
		t.Fatalf("value = %#v", cfg.Values["screenshots.location"])
	}
}

func TestPatchResetList(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1
reset = ["dock.tilesize"]
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.tilesize", Value: int64(44)}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Contains(got, "reset") {
		t.Fatalf("reset not removed:\n%s", got)
	}
	if !strings.Contains(got, "[dock]\ntilesize = 44") {
		t.Fatalf("value not inserted:\n%s", got)
	}

	if err := apply(t, path, Edit{Kind: ResetSetting, ID: "dock.tilesize"}); err != nil {
		t.Fatal(err)
	}
	got = read(t, path)
	if strings.Contains(got, "tilesize = 44") {
		t.Fatalf("value not removed on reset:\n%s", got)
	}
	if !strings.Contains(got, `reset = ["dock.tilesize"]`) {
		t.Fatalf("reset not added:\n%s", got)
	}

	if err := apply(t, path, Edit{Kind: UnresetSetting, ID: "dock.tilesize"}); err != nil {
		t.Fatal(err)
	}
	got = read(t, path)
	if strings.Contains(got, "reset") {
		t.Fatalf("reset not removed:\n%s", got)
	}
}

func TestPatchMultiLineResetList(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1
reset = [
  "dock.tilesize",
  "dock.autohide",
]
`)
	if err := apply(t, path, Edit{Kind: UnresetSetting, ID: "dock.tilesize"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, `reset = ["dock.autohide"]`) {
		t.Fatalf("reset list not rewritten:\n%s", got)
	}
}

func TestPatchDoesNotReorderUnchangedResetList(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1
reset = ["dock.tilesize", "dock.autohide"]

[dock]
position = "left"
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.position", Value: "right"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, `reset = ["dock.tilesize", "dock.autohide"]`) {
		t.Fatalf("reset list was rewritten:\n%s", got)
	}
}

func TestPatchRejectsInvalidValue(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", "config_version = 1\n")
	before := read(t, path)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.minimize_effect", Value: "explode"}); err == nil {
		t.Fatal("expected an error")
	}
	if got := read(t, path); got != before {
		t.Fatalf("file changed after invalid edit:\n%s", got)
	}
}

func TestPatchCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.HasPrefix(got, "config_version = 1") || !strings.Contains(got, "autohide = true") {
		t.Fatalf("unexpected new config:\n%s", got)
	}
	if _, err := Load(path, testCatalog(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPatchPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", "config_version = 1\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestPatchTableHeaderComment(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

[dock] # dock settings
autohide = false
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Count(got, "[dock]") != 1 {
		t.Fatalf("duplicate table inserted:\n%s", got)
	}
	if !strings.Contains(got, "autohide = true") {
		t.Fatalf("value not updated:\n%s", got)
	}
}

func TestPatchDottedKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1
dock.autohide = false
finder.show_hidden = true
`)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "dock.autohide = true") {
		t.Fatalf("dotted key not updated:\n%s", got)
	}
	if strings.Contains(got, "[dock]") {
		t.Fatalf("unexpected table inserted:\n%s", got)
	}
	if _, err := Load(path, testCatalog(t)); err != nil {
		t.Fatal(err)
	}
	if err := apply(t, path, Edit{Kind: RemoveValue, ID: "dock.autohide"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); strings.Contains(got, "dock.autohide") {
		t.Fatalf("dotted key not removed:\n%s", got)
	}
}

func TestPatchThroughSymlinkWritesTarget(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "real.toml", "config_version = 1\n\n[dock]\nautohide = false\n")
	link := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := apply(t, link, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, target); !strings.Contains(got, "autohide = true") {
		t.Fatalf("target not updated:\n%s", got)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink was replaced")
	}
}

func TestPatchNoOp(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", `config_version = 1

[dock]
autohide = true
`)
	before := read(t, path)
	if err := apply(t, path, Edit{Kind: SetValue, ID: "dock.autohide", Value: true}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != before {
		t.Fatalf("file changed on no-op:\n%s", got)
	}
}
