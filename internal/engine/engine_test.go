package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
	"github.com/metru/pear/internal/state"
)

type fakeAdapter struct {
	mu           sync.Mutex
	values       map[string]any
	found        map[string]bool
	ignoreWrites map[string]bool
	writeErr     map[string]error
	readErr      map[string]error
	writes       []string
	deletes      []string
	restarts     []string
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{
		values:       make(map[string]any),
		found:        make(map[string]bool),
		ignoreWrites: make(map[string]bool),
		writeErr:     make(map[string]error),
		readErr:      make(map[string]error),
	}
}

func (f *fakeAdapter) Supports(string) bool { return true }

func (f *fakeAdapter) Read(_ context.Context, setting catalog.Setting) (any, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.readErr[setting.ID]; err != nil {
		return nil, false, err
	}
	return f.values[setting.ID], f.found[setting.ID], nil
}

func (f *fakeAdapter) Write(_ context.Context, setting catalog.Setting, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, setting.ID)
	if err := f.writeErr[setting.ID]; err != nil {
		return err
	}
	if !f.ignoreWrites[setting.ID] {
		f.values[setting.ID] = v
		f.found[setting.ID] = true
	}
	return nil
}

func (f *fakeAdapter) Delete(_ context.Context, setting catalog.Setting) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, setting.ID)
	if err := f.writeErr[setting.ID]; err != nil {
		return err
	}
	if !f.ignoreWrites[setting.ID] {
		delete(f.values, setting.ID)
		delete(f.found, setting.ID)
	}
	return nil
}

func (f *fakeAdapter) Restart(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, target)
	return nil
}

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestBuildPlan(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.values["dock.tilesize"] = int64(44)
	adapter.found["dock.tilesize"] = true
	adapter.values["dock.show_recents"] = true
	adapter.found["dock.show_recents"] = true

	cfg := &config.Config{Values: map[string]any{
		"dock.autohide": true,
		"dock.tilesize": int64(44),
	}, Resets: []string{"dock.show_recents"}}

	eng := &Engine{Catalog: testCatalog(t), Adapter: adapter}
	plan, err := eng.BuildPlan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(plan.Changes))
	}
	if plan.Unchanged != 1 {
		t.Fatalf("unchanged = %d, want 1", plan.Unchanged)
	}
	if plan.Changes[0].Setting.ID != "dock.autohide" || plan.Changes[0].Reset {
		t.Fatalf("first change = %#v", plan.Changes[0])
	}
	if plan.Changes[1].Setting.ID != "dock.show_recents" || !plan.Changes[1].Reset {
		t.Fatalf("second change = %#v", plan.Changes[1])
	}
}

func TestBuildPlanOnly(t *testing.T) {
	cfg := &config.Config{Values: map[string]any{
		"dock.autohide": true,
		"dock.tilesize": int64(44),
	}}
	eng := &Engine{Catalog: testCatalog(t), Adapter: newFakeAdapter()}
	plan, err := eng.BuildPlan(context.Background(), cfg, []string{"dock.autohide"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(plan.Changes))
	}
	if plan.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", plan.Skipped)
	}
}

func TestBuildPlanUnknownSelector(t *testing.T) {
	eng := &Engine{Catalog: testCatalog(t), Adapter: newFakeAdapter()}
	_, err := eng.BuildPlan(context.Background(), &config.Config{Values: map[string]any{}}, []string{"dockx"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestBuildPlanReadError(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.readErr["dock.autohide"] = errors.New("boom")
	cfg := &config.Config{Values: map[string]any{"dock.autohide": true}}
	eng := &Engine{Catalog: testCatalog(t), Adapter: adapter}
	if _, err := eng.BuildPlan(context.Background(), cfg, nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestApplyWritesVerifiesAndSnapshots(t *testing.T) {
	adapter := newFakeAdapter()
	cfg := &config.Config{Values: map[string]any{
		"dock.autohide":     true,
		"dock.show_recents": false,
	}}
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Catalog: testCatalog(t), Adapter: adapter, Store: store}

	plan, err := eng.BuildPlan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := eng.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 2 {
		t.Fatalf("applied = %d, want 2", result.Applied)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("failures = %v", result.Failures)
	}
	if len(result.Restarts) != 1 || result.Restarts[0] != "Dock" {
		t.Fatalf("restarts = %v, want [Dock]", result.Restarts)
	}
	if result.SnapshotDir == "" {
		t.Fatal("no snapshot directory")
	}
	if adapter.values["dock.autohide"] != true {
		t.Fatalf("autohide = %#v", adapter.values["dock.autohide"])
	}

	snap, err := state.Load(result.SnapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("snapshot entries = %d, want 2", len(snap.Entries))
	}
	if snap.Entries[0].Existed {
		t.Fatalf("snapshot claims autohide existed: %#v", snap.Entries[0])
	}
}

func TestApplyVerifyFailure(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.ignoreWrites["dock.autohide"] = true
	cfg := &config.Config{Values: map[string]any{"dock.autohide": true}}
	eng := &Engine{Catalog: testCatalog(t), Adapter: adapter, Store: mustStore(t)}

	plan, err := eng.BuildPlan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := eng.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("failures = %v, want 1", result.Failures)
	}
	if result.Failures[0].Stage != "verify" {
		t.Fatalf("stage = %q, want verify", result.Failures[0].Stage)
	}
}

func TestApplyWriteFailureContinues(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.writeErr["dock.autohide"] = errors.New("nope")
	cfg := &config.Config{Values: map[string]any{
		"dock.autohide":     true,
		"dock.show_recents": false,
	}}
	eng := &Engine{Catalog: testCatalog(t), Adapter: adapter, Store: mustStore(t)}

	plan, err := eng.BuildPlan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := eng.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Stage != "write" {
		t.Fatalf("failures = %v", result.Failures)
	}
	if result.Applied != 1 {
		t.Fatalf("applied = %d, want 1", result.Applied)
	}
	if adapter.values["dock.show_recents"] != false {
		t.Fatalf("show_recents = %#v, want false", adapter.values["dock.show_recents"])
	}
}

func TestApplyNoChanges(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.values["dock.autohide"] = true
	adapter.found["dock.autohide"] = true
	cfg := &config.Config{Values: map[string]any{"dock.autohide": true}}
	eng := &Engine{Catalog: testCatalog(t), Adapter: adapter, Store: mustStore(t)}

	plan, err := eng.BuildPlan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := eng.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotDir != "" {
		t.Fatalf("snapshot created for no-op: %s", result.SnapshotDir)
	}
	if len(adapter.writes) != 0 {
		t.Fatalf("writes = %v, want none", adapter.writes)
	}
}

func TestRestore(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.values["dock.autohide"] = true
	adapter.found["dock.autohide"] = true
	adapter.values["dock.show_recents"] = true
	adapter.found["dock.show_recents"] = true

	cat := testCatalog(t)
	setting, _ := cat.Get("dock.autohide")
	snap := &state.Snapshot{
		Version: 1,
		Entries: []state.Entry{
			{
				ID:      "dock.autohide",
				Scope:   setting.Scope,
				Domain:  setting.Domain,
				Key:     setting.Key,
				Type:    setting.Type,
				Restart: setting.Restart,
				Existed: true,
				Value:   false,
			},
			{
				ID:      "dock.show_recents",
				Scope:   "user",
				Domain:  "com.apple.dock",
				Key:     "show-recents",
				Type:    "bool",
				Restart: []string{"Dock"},
				Existed: false,
			},
		},
	}
	eng := &Engine{Catalog: cat, Adapter: adapter}
	result, err := eng.Restore(context.Background(), snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 2 {
		t.Fatalf("applied = %d, want 2", result.Applied)
	}
	if adapter.values["dock.autohide"] != false {
		t.Fatalf("autohide = %#v, want false", adapter.values["dock.autohide"])
	}
	if adapter.found["dock.show_recents"] {
		t.Fatal("show_recents still present")
	}
	if len(result.Restarts) != 1 || result.Restarts[0] != "Dock" {
		t.Fatalf("restarts = %v", result.Restarts)
	}
}

func TestRestoreTakesSafetySnapshot(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.values["dock.autohide"] = true
	adapter.found["dock.autohide"] = true

	cat := testCatalog(t)
	setting, _ := cat.Get("dock.autohide")
	snap := &state.Snapshot{
		Version: 1,
		Entries: []state.Entry{{
			ID:      "dock.autohide",
			Scope:   setting.Scope,
			Domain:  setting.Domain,
			Key:     setting.Key,
			Type:    setting.Type,
			Restart: setting.Restart,
			Existed: true,
			Value:   false,
		}},
	}
	store := mustStore(t)
	eng := &Engine{Catalog: cat, Adapter: adapter, Store: store}
	if _, err := eng.Restore(context.Background(), snap, nil); err != nil {
		t.Fatal(err)
	}

	saved, _, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(saved.Entries))
	}
	if saved.Entries[0].Value != true {
		t.Fatalf("safety snapshot value = %#v, want true", saved.Entries[0].Value)
	}
}

func TestRestoreNoOp(t *testing.T) {
	adapter := newFakeAdapter()
	adapter.values["dock.autohide"] = false
	adapter.found["dock.autohide"] = true

	cat := testCatalog(t)
	setting, _ := cat.Get("dock.autohide")
	snap := &state.Snapshot{
		Version: 1,
		Entries: []state.Entry{{
			ID:      "dock.autohide",
			Scope:   setting.Scope,
			Domain:  setting.Domain,
			Key:     setting.Key,
			Type:    setting.Type,
			Restart: setting.Restart,
			Existed: true,
			Value:   false,
		}},
	}
	store := mustStore(t)
	eng := &Engine{Catalog: cat, Adapter: adapter, Store: store}
	result, err := eng.Restore(context.Background(), snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 0 || result.Unchanged != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Restarts) != 0 {
		t.Fatalf("restarts = %v, want none", result.Restarts)
	}
	if len(adapter.writes) != 0 {
		t.Fatalf("writes = %v, want none", adapter.writes)
	}
	if infos, _ := store.List(); len(infos) != 0 {
		t.Fatalf("snapshots = %v, want none", infos)
	}
}

func mustStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
