package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/metru/pear/internal/catalog"
)

func TestTeaSaveFlow(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("config_version = 1\n[dock]\nautohide = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Catalog: cat, ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	m.setValue("dock.autohide", true)

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("pear configure"))
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("Saved"))
	}, teatest.WithDuration(3*time.Second))

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("autohide = true")) {
		t.Fatalf("config not saved:\n%s", data)
	}
}
