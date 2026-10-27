package defaults

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"howett.net/plist"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/value"
)

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	mu      sync.Mutex
	calls   []call
	respond func(name string, args []string) ([]byte, []byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{name: name, args: append([]string(nil), args...)})
	f.mu.Unlock()
	if f.respond != nil {
		return f.respond(name, args)
	}
	return nil, nil, nil
}

func (f *fakeRunner) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newAdapter(runner Runner) *Adapter {
	return &Adapter{
		Defaults: "defaults",
		Killall:  "killall",
		Runner:   runner,
		cache:    make(map[string]*domain),
	}
}

func mustSetting(t *testing.T, id string) catalog.Setting {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	setting, ok := cat.Get(id)
	if !ok {
		t.Fatalf("%s not in catalog", id)
	}
	return setting
}

func exportPlist(t *testing.T, values map[string]any) []byte {
	t.Helper()
	data, err := plist.Marshal(values, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRead(t *testing.T) {
	data := exportPlist(t, map[string]any{
		"autohide": true,
		"tilesize": uint64(44),
	})
	runner := &fakeRunner{respond: func(name string, args []string) ([]byte, []byte, error) {
		if name == "defaults" && strings.Join(args, " ") == "export com.apple.dock -" {
			return data, nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected call: %s %v", name, args)
	}}
	adapter := newAdapter(runner)

	got, found, err := adapter.Read(context.Background(), mustSetting(t, "dock.autohide"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != true {
		t.Fatalf("Read = %#v, %v", got, found)
	}
	got, found, err = adapter.Read(context.Background(), mustSetting(t, "dock.tilesize"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != int64(44) {
		t.Fatalf("Read = %#v, %v", got, found)
	}
}

func TestReadCachesDomain(t *testing.T) {
	data := exportPlist(t, map[string]any{"autohide": true})
	runner := &fakeRunner{respond: func(string, []string) ([]byte, []byte, error) {
		return data, nil, nil
	}}
	adapter := newAdapter(runner)
	setting := mustSetting(t, "dock.autohide")

	if _, _, err := adapter.Read(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Read(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	if calls := runner.count(); calls != 1 {
		t.Fatalf("export called %d times, want 1", calls)
	}

	if err := adapter.Write(context.Background(), setting, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Read(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	if calls := runner.count(); calls != 3 {
		t.Fatalf("calls = %d, want 3 (export, write, export)", calls)
	}
}

func TestReadMissingDomain(t *testing.T) {
	runner := &fakeRunner{respond: func(string, []string) ([]byte, []byte, error) {
		return nil, []byte("Domain com.apple.dock does not exist"), errors.New("exit status 1")
	}}
	adapter := newAdapter(runner)
	got, found, err := adapter.Read(context.Background(), mustSetting(t, "dock.autohide"))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("found = true, value = %#v", got)
	}
}

func TestReadBadStoredType(t *testing.T) {
	data := exportPlist(t, map[string]any{"autohide": "yes"})
	runner := &fakeRunner{respond: func(string, []string) ([]byte, []byte, error) {
		return data, nil, nil
	}}
	adapter := newAdapter(runner)
	if _, _, err := adapter.Read(context.Background(), mustSetting(t, "dock.autohide")); err == nil {
		t.Fatal("expected a type error")
	}
}

func TestWriteCommands(t *testing.T) {
	tests := []struct {
		id    string
		value any
		args  string
	}{
		{"dock.autohide", true, "write com.apple.dock autohide -bool true"},
		{"dock.tilesize", int64(44), "write com.apple.dock tilesize -int 44"},
		{"dock.autohide_delay", 0.25, "write com.apple.dock autohide-delay -float 0.25"},
		{"dock.minimize_effect", "scale", "write com.apple.dock mineffect -string scale"},
		{"dock.position", "left", "write com.apple.dock orientation -string left"},
		{"screenshots.location", "/Users/me/Pictures", "write com.apple.screencapture location -string /Users/me/Pictures"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			runner := &fakeRunner{}
			adapter := newAdapter(runner)
			if err := adapter.Write(context.Background(), mustSetting(t, test.id), test.value); err != nil {
				t.Fatal(err)
			}
			got := strings.Join(runner.last().args, " ")
			if got != test.args {
				t.Fatalf("args = %q, want %q", got, test.args)
			}
		})
	}
}

func TestWriteHostScope(t *testing.T) {
	runner := &fakeRunner{}
	adapter := newAdapter(runner)
	setting := catalog.Setting{
		ID:      "test.host",
		Scope:   "host",
		Domain:  "com.example.test",
		Key:     "enabled",
		Type:    value.Bool,
		Adapter: "defaults",
	}
	if err := adapter.Write(context.Background(), setting, true); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.last().args, " ")
	want := "-currentHost write com.example.test enabled -bool true"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestWriteStringList(t *testing.T) {
	runner := &fakeRunner{}
	adapter := newAdapter(runner)
	setting := catalog.Setting{
		ID:      "test.list",
		Scope:   "user",
		Domain:  "com.example.test",
		Key:     "items",
		Type:    value.StringList,
		Adapter: "defaults",
	}
	if err := adapter.Write(context.Background(), setting, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.last().args, " ")
	want := "write com.example.test items -array a b"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestDelete(t *testing.T) {
	runner := &fakeRunner{}
	adapter := newAdapter(runner)
	if err := adapter.Delete(context.Background(), mustSetting(t, "dock.tilesize")); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.last().args, " ")
	if got != "delete com.apple.dock tilesize" {
		t.Fatalf("args = %q", got)
	}
}

func TestDeleteMissingKeyIsOK(t *testing.T) {
	tests := []string{
		"The domain/default pair of (com.apple.dock, tilesize) does not exist",
		"Domain (com.apple.menuextra.clock) not found.\nDefaults have not been changed.",
	}
	for _, stderr := range tests {
		runner := &fakeRunner{respond: func(string, []string) ([]byte, []byte, error) {
			return nil, []byte(stderr), errors.New("exit status 1")
		}}
		adapter := newAdapter(runner)
		if err := adapter.Delete(context.Background(), mustSetting(t, "dock.tilesize")); err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
	}
}

func TestRestart(t *testing.T) {
	runner := &fakeRunner{}
	adapter := newAdapter(runner)
	if err := adapter.Restart(context.Background(), "Dock"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.last().args, " "); got != "Dock" {
		t.Fatalf("args = %q, want Dock", got)
	}
}

func TestRestartNotRunningIsOK(t *testing.T) {
	runner := &fakeRunner{respond: func(string, []string) ([]byte, []byte, error) {
		return nil, []byte("No matching processes belonging to you were found"), errors.New("exit status 1")
	}}
	adapter := newAdapter(runner)
	if err := adapter.Restart(context.Background(), "Finder"); err != nil {
		t.Fatalf("Restart = %v, want nil", err)
	}
}

func TestRestartError(t *testing.T) {
	runner := &fakeRunner{respond: func(string, []string) ([]byte, []byte, error) {
		return nil, []byte("Operation not permitted"), errors.New("exit status 1")
	}}
	adapter := newAdapter(runner)
	if err := adapter.Restart(context.Background(), "Finder"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUnsupportedScope(t *testing.T) {
	adapter := newAdapter(&fakeRunner{})
	setting := catalog.Setting{ID: "x.y", Scope: "system", Domain: "d", Key: "k", Type: value.Bool}
	if adapter.Supports(setting.Scope) {
		t.Fatal("system scope should not be supported in this milestone")
	}
	if _, _, err := adapter.Read(context.Background(), setting); err == nil {
		t.Fatal("expected an error")
	}
}
