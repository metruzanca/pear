package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{ID: "dock.autohide", Scope: "user", Domain: "com.apple.dock", Key: "autohide", Type: "bool", Existed: true, Value: false},
		{ID: "finder.show_hidden", Scope: "user", Domain: "com.apple.finder", Key: "AppleShowAllFiles", Type: "bool", Existed: false},
	}
	dir, err := store.Save(entries)
	if err != nil {
		t.Fatal(err)
	}

	snap, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != 1 {
		t.Fatalf("version = %d", snap.Version)
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("entries = %d", len(snap.Entries))
	}
	if snap.Entries[0].ID != "dock.autohide" || snap.Entries[0].Value != false {
		t.Fatalf("entry = %#v", snap.Entries[0])
	}
	if snap.Entries[1].Existed {
		t.Fatalf("entry = %#v", snap.Entries[1])
	}

	info, err := os.Stat(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v", info.Mode().Perm())
	}
}

func TestListAndLatest(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if infos, err := store.List(); err != nil || len(infos) != 0 {
		t.Fatalf("List = %v, %v", infos, err)
	}
	if _, _, err := store.Latest(); err == nil {
		t.Fatal("Latest on empty store should fail")
	}

	first, err := store.Save([]Entry{{ID: "dock.autohide"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Save([]Entry{{ID: "dock.tilesize"}})
	if err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos = %d", len(infos))
	}

	snap, path, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if path != second && path != first {
		t.Fatalf("latest path = %s", path)
	}
	if len(snap.Entries) != 1 {
		t.Fatalf("entries = %d", len(snap.Entries))
	}

	opened, openedPath, err := store.Open(filepath.Base(first))
	if err != nil {
		t.Fatal(err)
	}
	if openedPath != first {
		t.Fatalf("opened path = %s, want %s", openedPath, first)
	}
	if opened.Entries[0].ID != "dock.autohide" {
		t.Fatalf("entry = %#v", opened.Entries[0])
	}
}

func TestLoadRejectsBadVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("expected version error")
	}
}

func TestStringListRoundTrip(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.Save([]Entry{{
		ID:      "test.list",
		Type:    "string_list",
		Existed: true,
		Value:   []string{"a", "b"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := snap.Entries[0].Value.([]any)
	if !ok {
		t.Fatalf("value = %#v", snap.Entries[0].Value)
	}
	if len(items) != 2 || items[0] != "a" || items[1] != "b" {
		t.Fatalf("items = %#v", items)
	}
}
