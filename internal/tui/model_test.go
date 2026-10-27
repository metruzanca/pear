package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
)

type fakeAdapter struct {
	values map[string]any
	found  map[string]bool
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{values: map[string]any{}, found: map[string]bool{}}
}

func (f *fakeAdapter) Supports(string) bool { return true }

func (f *fakeAdapter) Read(_ context.Context, s catalog.Setting) (any, bool, error) {
	return f.values[s.ID], f.found[s.ID], nil
}

func (f *fakeAdapter) Write(_ context.Context, s catalog.Setting, v any) error {
	f.values[s.ID] = v
	f.found[s.ID] = true
	return nil
}

func (f *fakeAdapter) Delete(_ context.Context, s catalog.Setting) error {
	delete(f.values, s.ID)
	delete(f.found, s.ID)
	return nil
}

func (f *fakeAdapter) Restart(context.Context, string) error { return nil }

func newTestModel(t *testing.T, content string) (*Model, string, *fakeAdapter) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := newFakeAdapter()
	m, err := New(Options{
		Catalog:    cat,
		ConfigPath: path,
		Adapter:    adapter,
		CanApply:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, path, adapter
}

func mustResolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := config.ResolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func findRow(t *testing.T, m *Model, id string) *row {
	t.Helper()
	for _, r := range m.rows {
		if r.setting.ID == id {
			return r
		}
	}
	t.Fatalf("row %s not found", id)
	return nil
}

func TestNewRows(t *testing.T) {
	m, _, adapter := newTestModel(t, `config_version = 1
reset = ["dock.tilesize"]

[dock]
autohide = true
`)
	adapter.values["dock.autohide"] = false
	adapter.found["dock.autohide"] = true
	adapter.values["dock.tilesize"] = int64(44)
	adapter.found["dock.tilesize"] = true
	m.current = map[string]currentValue{
		"dock.autohide": {value: false, found: true, known: true},
		"dock.tilesize": {value: int64(44), found: true, known: true},
	}
	m.rebuildRows()

	if r := findRow(t, m, "dock.autohide"); r.status != statusSet || r.pending != true {
		t.Fatalf("autohide row = %+v", r)
	}
	if r := findRow(t, m, "dock.tilesize"); r.status != statusReset || r.pending != true {
		t.Fatalf("tilesize row = %+v", r)
	}
	if r := findRow(t, m, "finder.show_hidden"); r.status != statusUnset || r.pending {
		t.Fatalf("finder row = %+v", r)
	}
	if got := m.pendingCount(); got != 2 {
		t.Fatalf("pending = %d, want 2", got)
	}
}

func TestPendingEdits(t *testing.T) {
	m, _, _ := newTestModel(t, `config_version = 1
reset = ["dock.tilesize"]

[dock]
autohide = true
`)
	if edits := m.pendingEdits(); len(edits) != 0 {
		t.Fatalf("initial edits = %v (initial resets %v, state resets %v, reset origins %v)", edits, m.initial.resets, m.state.Resets, m.state.ResetOrigins)
	}

	m.setValue("dock.autohide", false)
	edits := m.pendingEdits()
	if len(edits) != 1 || edits[0].Kind != config.SetValue || edits[0].ID != "dock.autohide" {
		t.Fatalf("edits = %+v", edits)
	}

	m.setValue("dock.tilesize", int64(44))
	edits = m.pendingEdits()
	if len(edits) != 2 {
		t.Fatalf("edits = %+v", edits)
	}
	found := false
	for _, edit := range edits {
		if edit.ID == "dock.tilesize" && edit.Kind == config.SetValue {
			found = true
		}
	}
	if !found {
		t.Fatalf("tilesize set edit missing: %+v", edits)
	}

	m.resetSetting("dock.autohide")
	edits = m.pendingEdits()
	found = false
	for _, edit := range edits {
		if edit.ID == "dock.autohide" && edit.Kind == config.ResetSetting {
			found = true
		}
	}
	if !found {
		t.Fatalf("autohide reset edit missing: %+v", edits)
	}
}

func TestRemoveRootValueRevealsInclude(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "base.toml")
	if err := os.WriteFile(base, []byte("[finder]\nshow_hidden = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("config_version = 1\ninclude = [\"base.toml\"]\n[finder]\nshow_hidden = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Catalog: cat, ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	m.removeRootDirective("finder.show_hidden")
	if v, ok := m.state.Values["finder.show_hidden"]; !ok || v != true {
		t.Fatalf("value after unset = %#v, %v", v, ok)
	}
	if !samePath(m.state.Origins["finder.show_hidden"], mustResolvePath(t, base)) {
		t.Fatalf("origin = %s, want base", m.state.Origins["finder.show_hidden"])
	}
}

func TestCannotUnsetInherited(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.toml"), []byte("[finder]\nshow_hidden = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("config_version = 1\ninclude = [\"base.toml\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Catalog: cat, ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	msg := m.removeRootDirective("finder.show_hidden")
	if msg == "" {
		t.Fatal("expected an explanation")
	}
	if !strings.Contains(msg, "base.toml") {
		t.Fatalf("message = %q", msg)
	}
	if v, ok := m.state.Values["finder.show_hidden"]; !ok || v != true {
		t.Fatalf("state changed: %#v", m.state.Values["finder.show_hidden"])
	}
}

func TestArrowKeysAndNoVim(t *testing.T) {
	m, _, _ := newTestModel(t, "config_version = 1\n[dock]\nautohide = true\ntilesize = 44\n")
	m.list.Select(0)
	first := m.selectedRow().setting.ID

	down := tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	_, _ = m.Update(down)
	if m.selectedRow().setting.ID == first {
		t.Fatal("down arrow did not move selection")
	}
	current := m.selectedRow().setting.ID

	vimDown := tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"})
	_, _ = m.Update(vimDown)
	if m.selectedRow().setting.ID != current {
		t.Fatal("j moved selection; vim binding still active")
	}

	if got := m.list.KeyMap.CursorUp.Keys(); len(got) != 1 || got[0] != "up" {
		t.Fatalf("cursor up keys = %v", got)
	}
	if got := m.list.KeyMap.CursorDown.Keys(); len(got) != 1 || got[0] != "down" {
		t.Fatalf("cursor down keys = %v", got)
	}

	km := noVimKeyMap()
	if got := km.Select.Up.Keys(); len(got) != 1 || got[0] != "up" {
		t.Fatalf("select up keys = %v", got)
	}
	if got := km.Select.Down.Keys(); len(got) != 1 || got[0] != "down" {
		t.Fatalf("select down keys = %v", got)
	}
	for _, key := range km.Text.NewLine.Keys() {
		if key == "ctrl+j" {
			t.Fatal("ctrl+j newline binding still active")
		}
	}
}

func TestEnterOpensEditor(t *testing.T) {
	m, _, _ := newTestModel(t, "config_version = 1\n[dock]\nautohide = true\n")
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.screen != screenEdit {
		t.Fatalf("screen = %v, want edit", m.screen)
	}
	if m.edit == nil || m.edit.setting.ID != "dock.autohide" {
		t.Fatalf("edit = %+v", m.edit)
	}
	if view := m.View().Content; !strings.Contains(view, "dock.autohide") {
		t.Fatalf("view missing setting:\n%s", view)
	}
}

func TestEditValues(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	tilesize, _ := cat.Get("dock.tilesize")
	edit := newEditModel(&row{setting: tilesize, status: statusSet, value: int64(44)}, 80, true)
	edit.strValue = "48"
	v, err := edit.Value()
	if err != nil || v != int64(48) {
		t.Fatalf("int value = %#v, %v", v, err)
	}
	edit.strValue = "nope"
	if _, err := edit.Value(); err == nil {
		t.Fatal("expected parse error")
	}

	location, _ := cat.Get("screenshots.location")
	pathEdit := newEditModel(&row{setting: location, status: statusUnset}, 80, true)
	pathEdit.strValue = "~/Pictures"
	v, err = pathEdit.Value()
	if err != nil || v != "~/Pictures" {
		t.Fatalf("path value = %#v, %v", v, err)
	}
	pathEdit.strValue = "relative/path"
	if _, err := pathEdit.Value(); err == nil {
		t.Fatal("expected path error")
	}

	enabled, _ := cat.Get("dock.autohide")
	boolEdit := newEditModel(&row{setting: enabled, status: statusSet, value: true}, 80, true)
	boolEdit.boolValue = false
	v, err = boolEdit.Value()
	if err != nil || v != false {
		t.Fatalf("bool value = %#v, %v", v, err)
	}
}

func TestSaveCmdWritesConfig(t *testing.T) {
	m, path, _ := newTestModel(t, "config_version = 1\n[dock]\nautohide = false\n")
	m.setValue("dock.autohide", true)
	edits := m.pendingEdits()
	msg := saveCmd(path, m.cat, edits)()
	saved, ok := msg.(savedMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if saved.err != nil {
		t.Fatal(saved.err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "autohide = true") {
		t.Fatalf("config not updated:\n%s", data)
	}
	if saved.cfg == nil || saved.cfg.Values["dock.autohide"] != true {
		t.Fatalf("saved cfg = %+v", saved.cfg)
	}
}

func TestSaveMissingConfigClearsDirty(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pear", "config.toml")
	m, err := New(Options{Catalog: cat, ConfigPath: path, AllowCreate: true})
	if err != nil {
		t.Fatal(err)
	}
	m.setValue("dock.autohide", true)
	msg := saveCmd(m.path, m.cat, m.pendingEdits())()
	saved, ok := msg.(savedMsg)
	if !ok || saved.err != nil {
		t.Fatalf("save = %#v", msg)
	}
	updated, _ := m.handleSaved(saved)
	m = updated.(*Model)
	if edits := m.pendingEdits(); len(edits) != 0 {
		t.Fatalf("still dirty after save: %+v", edits)
	}
	if !samePath(m.path, saved.cfg.Path) {
		t.Fatalf("path = %q, cfg path = %q", m.path, saved.cfg.Path)
	}
}

func TestTabCyclesStatusFilter(t *testing.T) {
	m, _, _ := newTestModel(t, "config_version = 1\n[dock]\nautohide = true\n")
	if m.filter != filterAll {
		t.Fatalf("initial filter = %v", m.filter)
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if m.filter != filterConfigured {
		t.Fatalf("filter after tab = %v", m.filter)
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if m.filter != filterUnset {
		t.Fatalf("filter after second tab = %v", m.filter)
	}
}

func TestListFilterModes(t *testing.T) {
	m, _, _ := newTestModel(t, "config_version = 1\n[dock]\nautohide = true\n")
	m.filter = filterUnset
	for _, r := range m.visibleRows() {
		if r.status != statusUnset {
			t.Fatalf("visible row %s is not unset", r.setting.ID)
		}
	}
	m.filter = filterConfigured
	seen := false
	for _, r := range m.visibleRows() {
		if r.status != statusUnset {
			seen = true
		}
	}
	if !seen {
		t.Fatal("configured filter returned nothing")
	}
}

var _ list.Item = (*row)(nil)
