package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/metru/pear/internal/adapter"
	"github.com/metru/pear/internal/adapter/defaults"
	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
	"github.com/metru/pear/internal/engine"
	"github.com/metru/pear/internal/state"
)

type currentLoadedMsg struct {
	values map[string]currentValue
}

type savedMsg struct {
	cfg    *config.Config
	edits  int
	backup string
	err    error
}

type applyDoneMsg struct {
	result *engine.ApplyResult
	err    error
}

func loadCurrentCmd(cat *catalog.Catalog, a adapter.Adapter) tea.Cmd {
	if a == nil {
		return nil
	}
	return func() tea.Msg {
		if refresher, ok := a.(interface{ Refresh() }); ok {
			refresher.Refresh()
		}
		ctx := context.Background()
		values := make(map[string]currentValue, cat.Count())
		for _, setting := range cat.All() {
			if !a.Supports(setting.Scope) {
				values[setting.ID] = currentValue{}
				continue
			}
			v, found, err := a.Read(ctx, setting)
			values[setting.ID] = currentValue{
				value: v,
				found: found,
				known: err == nil,
				err:   err,
			}
		}
		return currentLoadedMsg{values: values}
	}
}

func saveCmd(path string, cat *catalog.Catalog, edits []config.Edit) tea.Cmd {
	return func() tea.Msg {
		backup, err := backupConfig(path)
		if err != nil {
			return savedMsg{err: fmt.Errorf("back up config: %w", err)}
		}
		if err := config.ApplyEdits(path, cat, edits); err != nil {
			return savedMsg{err: err}
		}
		cfg, err := config.Load(path, cat)
		if err != nil {
			return savedMsg{err: err}
		}
		return savedMsg{cfg: cfg, edits: len(edits), backup: backup}
	}
}

func backupConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	root := filepath.Join(state.DefaultDir(), "config-backups")
	dir := filepath.Join(root, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, filepath.Base(path))
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func applyCmd(cat *catalog.Catalog, path string, only []string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load(path, cat)
		if err != nil {
			return applyDoneMsg{err: err}
		}
		store, err := state.Default()
		if err != nil {
			return applyDoneMsg{err: err}
		}
		eng := &engine.Engine{
			Catalog: cat,
			Adapter: defaults.New(),
			Store:   store,
		}
		plan, err := eng.BuildPlan(context.Background(), cfg, only)
		if err != nil {
			return applyDoneMsg{err: err}
		}
		if !plan.HasChanges() {
			return applyDoneMsg{result: &engine.ApplyResult{
				Unchanged: plan.Unchanged,
				Skipped:   plan.Skipped,
			}}
		}
		result, err := eng.Apply(context.Background(), plan)
		return applyDoneMsg{result: result, err: err}
	}
}
