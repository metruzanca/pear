// Package engine turns a validated config into a plan, applies it, verifies
// it, and can roll it back from a snapshot.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/metru/pear/internal/adapter"
	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
	"github.com/metru/pear/internal/state"
	"github.com/metru/pear/internal/value"
)

// Engine plans and applies settings.
type Engine struct {
	Catalog *catalog.Catalog
	Adapter adapter.Adapter
	Store   *state.Store
}

// Change is one setting that differs from the config.
type Change struct {
	Setting catalog.Setting
	Desired any
	Current any
	Existed bool
	Reset   bool
}

// Action returns "set" or "reset".
func (c Change) Action() string {
	if c.Reset {
		return "reset"
	}
	return "set"
}

// Plan is the result of comparing config to the current system.
type Plan struct {
	Changes   []Change
	Unchanged int
	Skipped   int
}

// HasChanges reports whether the plan would modify anything.
func (p *Plan) HasChanges() bool {
	return len(p.Changes) > 0
}

// Failure describes a write, restart, or verification error.
type Failure struct {
	ID    string
	Stage string
	Err   error
}

// String implements fmt.Stringer.
func (f Failure) String() string {
	if f.ID == "" {
		return fmt.Sprintf("%s: %v", f.Stage, f.Err)
	}
	return fmt.Sprintf("%s: %s: %v", f.Stage, f.ID, f.Err)
}

// ApplyResult summarises an apply or restore.
type ApplyResult struct {
	Applied     int
	Unchanged   int
	Skipped     int
	Restarts    []string
	SnapshotDir string
	Failures    []Failure
}

// BuildPlan compares cfg with the current system.
func (e *Engine) BuildPlan(ctx context.Context, cfg *config.Config, only []string) (*Plan, error) {
	selectors, err := e.validateSelectors(only)
	if err != nil {
		return nil, err
	}

	plan := &Plan{}

	ids := make([]string, 0, len(cfg.Values))
	for id := range cfg.Values {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if !matches(id, selectors) {
			plan.Skipped++
			continue
		}
		setting, ok := e.Catalog.Get(id)
		if !ok {
			return nil, fmt.Errorf("%s: not in catalog", id)
		}
		if !e.Adapter.Supports(setting.Scope) {
			return nil, fmt.Errorf("%s: %s scope is not supported yet", id, setting.Scope)
		}
		current, found, err := e.Adapter.Read(ctx, setting)
		if err != nil {
			return nil, err
		}
		desired := cfg.Values[id]
		if found && value.Equal(current, desired) {
			plan.Unchanged++
			continue
		}
		plan.Changes = append(plan.Changes, Change{
			Setting: setting,
			Desired: desired,
			Current: current,
			Existed: found,
		})
	}

	for _, id := range cfg.Resets {
		if !matches(id, selectors) {
			plan.Skipped++
			continue
		}
		setting, ok := e.Catalog.Get(id)
		if !ok {
			return nil, fmt.Errorf("%s: not in catalog", id)
		}
		if !e.Adapter.Supports(setting.Scope) {
			return nil, fmt.Errorf("%s: %s scope is not supported yet", id, setting.Scope)
		}
		current, found, err := e.Adapter.Read(ctx, setting)
		if err != nil {
			return nil, err
		}
		if !found {
			plan.Unchanged++
			continue
		}
		plan.Changes = append(plan.Changes, Change{
			Setting: setting,
			Current: current,
			Existed: true,
			Reset:   true,
		})
	}

	sort.Slice(plan.Changes, func(i, j int) bool {
		return plan.Changes[i].Setting.ID < plan.Changes[j].Setting.ID
	})
	return plan, nil
}

// Apply writes a plan, restarts affected applications, and verifies the result.
func (e *Engine) Apply(ctx context.Context, plan *Plan) (*ApplyResult, error) {
	if !plan.HasChanges() {
		return &ApplyResult{Unchanged: plan.Unchanged, Skipped: plan.Skipped}, nil
	}

	entries := make([]state.Entry, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		entries = append(entries, state.Entry{
			ID:      change.Setting.ID,
			Scope:   change.Setting.Scope,
			Domain:  change.Setting.Domain,
			Key:     change.Setting.Key,
			Type:    change.Setting.Type,
			Restart: change.Setting.Restart,
			Existed: change.Existed,
			Value:   change.Current,
		})
	}

	snapshotDir := ""
	if e.Store != nil {
		dir, err := e.Store.Save(entries)
		if err != nil {
			return nil, fmt.Errorf("refusing to apply without a snapshot: %w", err)
		}
		snapshotDir = dir
	}

	result := e.execute(ctx, plan.Changes, plan.Unchanged, plan.Skipped)
	result.SnapshotDir = snapshotDir
	return result, nil
}

// Restore writes the values recorded in snap, without taking a new snapshot.
func (e *Engine) Restore(ctx context.Context, snap *state.Snapshot, only []string) (*ApplyResult, error) {
	selectors, err := e.validateSelectors(only)
	if err != nil {
		return nil, err
	}

	var changes []Change
	skipped := 0
	unchanged := 0
	for _, entry := range snap.Entries {
		if !matches(entry.ID, selectors) {
			skipped++
			continue
		}
		change, err := restoreChange(entry)
		if err != nil {
			return nil, err
		}
		current, found, err := e.Adapter.Read(ctx, change.Setting)
		if err != nil {
			return nil, fmt.Errorf("refusing to restore without checking %s: %w", change.Setting.ID, err)
		}
		if change.Reset {
			if !found {
				unchanged++
				continue
			}
		} else if found && value.Equal(current, change.Desired) {
			unchanged++
			continue
		}
		change.Current = current
		change.Existed = found
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Setting.ID < changes[j].Setting.ID
	})

	// Restoring is itself a change. Save what is currently there so a mistaken
	// restore can be undone with another restore.
	if len(changes) > 0 && e.Store != nil {
		entries := make([]state.Entry, 0, len(changes))
		for _, change := range changes {
			entries = append(entries, state.Entry{
				ID:      change.Setting.ID,
				Scope:   change.Setting.Scope,
				Domain:  change.Setting.Domain,
				Key:     change.Setting.Key,
				Type:    change.Setting.Type,
				Restart: change.Setting.Restart,
				Existed: change.Existed,
				Value:   change.Current,
			})
		}
		if _, err := e.Store.Save(entries); err != nil {
			return nil, fmt.Errorf("refusing to restore without a snapshot: %w", err)
		}
	}

	return e.execute(ctx, changes, unchanged, skipped), nil
}

func restoreChange(entry state.Entry) (Change, error) {
	setting := catalog.Setting{
		ID:      entry.ID,
		Scope:   entry.Scope,
		Domain:  entry.Domain,
		Key:     entry.Key,
		Type:    entry.Type,
		Restart: entry.Restart,
	}
	change := Change{Setting: setting, Reset: !entry.Existed}
	if entry.Existed {
		normalized, err := value.Normalize(entry.Value, entry.Type)
		if err != nil {
			return Change{}, fmt.Errorf("restore %s: %w", entry.ID, err)
		}
		change.Desired = normalized
		change.Existed = true
	}
	return change, nil
}

func (e *Engine) execute(ctx context.Context, changes []Change, unchanged, skipped int) *ApplyResult {
	result := &ApplyResult{Unchanged: unchanged, Skipped: skipped}
	if len(changes) == 0 {
		return result
	}

	var written []Change
	for _, change := range changes {
		var err error
		if change.Reset {
			err = e.Adapter.Delete(ctx, change.Setting)
		} else {
			err = e.Adapter.Write(ctx, change.Setting, change.Desired)
		}
		if err != nil {
			result.Failures = append(result.Failures, Failure{
				ID:    change.Setting.ID,
				Stage: "write",
				Err:   err,
			})
			continue
		}
		written = append(written, change)
		result.Applied++
	}

	// Restart once per target, after all writes, and before verification.
	restarts := restartTargets(written)
	for _, target := range restarts {
		if err := e.Adapter.Restart(ctx, target); err != nil {
			result.Failures = append(result.Failures, Failure{
				Stage: "restart",
				Err:   err,
			})
		}
	}
	result.Restarts = restarts

	for _, change := range written {
		current, found, err := e.Adapter.Read(ctx, change.Setting)
		if err != nil {
			result.Failures = append(result.Failures, Failure{
				ID:    change.Setting.ID,
				Stage: "verify",
				Err:   err,
			})
			continue
		}
		if change.Reset {
			if found {
				result.Failures = append(result.Failures, Failure{
					ID:    change.Setting.ID,
					Stage: "verify",
					Err:   fmt.Errorf("still set to %s after delete", value.Display(current)),
				})
			}
			continue
		}
		if !found {
			result.Failures = append(result.Failures, Failure{
				ID:    change.Setting.ID,
				Stage: "verify",
				Err:   fmt.Errorf("value disappeared after write"),
			})
			continue
		}
		if !value.Equal(current, change.Desired) {
			result.Failures = append(result.Failures, Failure{
				ID:    change.Setting.ID,
				Stage: "verify",
				Err:   fmt.Errorf("wanted %s, got %s", value.Display(change.Desired), value.Display(current)),
			})
		}
	}
	return result
}

func restartTargets(changes []Change) []string {
	seen := make(map[string]bool)
	var targets []string
	for _, change := range changes {
		for _, target := range change.Setting.Restart {
			if !seen[target] {
				seen[target] = true
				targets = append(targets, target)
			}
		}
	}
	sort.Strings(targets)
	return targets
}

func (e *Engine) validateSelectors(only []string) ([]string, error) {
	selectors := make([]string, 0, len(only))
	for _, selector := range only {
		selector = strings.TrimSpace(selector)
		if selector == "" {
			continue
		}
		if !e.Catalog.HasPrefix(selector) {
			suggestion := ""
			if s := e.Catalog.Suggest(selector); s != "" {
				suggestion = fmt.Sprintf(" (did you mean %q?)", s)
			}
			return nil, fmt.Errorf("unknown selector %q%s", selector, suggestion)
		}
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

func matches(id string, selectors []string) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, selector := range selectors {
		if id == selector || strings.HasPrefix(id, selector+".") {
			return true
		}
	}
	return false
}
