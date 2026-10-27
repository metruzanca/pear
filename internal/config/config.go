// Package config loads and validates Pear's TOML configuration.
//
// Config keys are catalog ids: a table named dock with a key named autohide
// becomes "dock.autohide". Unknown keys are errors, not warnings, so a typo in
// a dotfiles repo fails loudly instead of silently doing nothing.
//
// Files are layered. Includes are applied in order, then the including file.
// A value in a later layer cancels an earlier reset, and a reset in a later
// layer cancels an earlier value. Setting and resetting the same id within one
// file is an error.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/value"
)

const currentVersion = 1

// State is an effective set of values and resets with their source files.
type State struct {
	Values       map[string]any
	Resets       []string
	Origins      map[string]string
	ResetOrigins map[string]string
}

// HasReset reports whether id is reset in the state.
func (s State) HasReset(id string) bool {
	return slices.Contains(s.Resets, id)
}

// Config is a fully resolved, validated configuration.
type Config struct {
	Path         string
	Version      int
	Values       map[string]any
	Resets       []string
	Origins      map[string]string
	ResetOrigins map[string]string
	// Inherited is the effective state after loading the root file's includes,
	// before the root file's own values and resets. The editor uses it to
	// reveal what a root value is overriding.
	Inherited State
}

// DefaultPath returns the config path from PEAR_CONFIG, XDG_CONFIG_HOME, or
// ~/.config/pear/config.toml, in that order.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv("PEAR_CONFIG")); p != "" {
		return ExpandPath(p)
	}
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		return filepath.Join(x, "pear", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "pear", "config.toml")
	}
	return filepath.Join(home, ".config", "pear", "config.toml")
}

// ExpandPath expands a leading ~ to the current user's home directory.
func ExpandPath(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// ResolvePath makes a config path absolute and follows symlinks. For a missing
// file it still resolves the parent directory, so the path stays canonical
// before and after the file is created.
func ResolvePath(path string) (string, error) {
	abs, err := filepath.Abs(ExpandPath(path))
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(resolvedDir, filepath.Base(abs)), nil
	}
	return abs, nil
}

// Load reads path, resolves includes, and validates every setting against cat.
func Load(path string, cat *catalog.Catalog) (*Config, error) {
	abs, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	l := &loader{
		cat:  cat,
		seen: make(map[string]bool),
	}
	final, err := l.loadFile(abs, true, 0)
	if err != nil {
		return nil, err
	}
	if !l.rootHasVersion {
		return nil, fmt.Errorf("%s: missing required config_version = %d", abs, currentVersion)
	}

	state := final.state()
	inherited := emptyState()
	if l.inherited != nil {
		inherited = l.inherited.state()
	}

	return &Config{
		Path:         abs,
		Version:      currentVersion,
		Values:       state.Values,
		Resets:       state.Resets,
		Origins:      state.Origins,
		ResetOrigins: state.ResetOrigins,
		Inherited:    inherited,
	}, nil
}

func emptyState() State {
	return State{
		Values:       map[string]any{},
		Origins:      map[string]string{},
		Resets:       nil,
		ResetOrigins: map[string]string{},
	}
}

type layer struct {
	values       map[string]any
	resets       map[string]bool
	valueOrigins map[string]string
	resetOrigins map[string]string
}

func newLayer() *layer {
	return &layer{
		values:       make(map[string]any),
		resets:       make(map[string]bool),
		valueOrigins: make(map[string]string),
		resetOrigins: make(map[string]string),
	}
}

func (l *layer) clone() *layer {
	c := newLayer()
	for id, v := range l.values {
		c.values[id] = v
	}
	for id, v := range l.valueOrigins {
		c.valueOrigins[id] = v
	}
	for id := range l.resets {
		c.resets[id] = true
	}
	for id, v := range l.resetOrigins {
		c.resetOrigins[id] = v
	}
	return c
}

// merge applies src on top of l.
func (l *layer) merge(src *layer) {
	for id, v := range src.values {
		l.values[id] = v
		l.valueOrigins[id] = src.valueOrigins[id]
		delete(l.resets, id)
		delete(l.resetOrigins, id)
	}
	for id := range src.resets {
		delete(l.values, id)
		delete(l.valueOrigins, id)
		l.resets[id] = true
		l.resetOrigins[id] = src.resetOrigins[id]
	}
}

func (l *layer) state() State {
	ids := make([]string, 0, len(l.resets))
	for id := range l.resets {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	values := make(map[string]any, len(l.values))
	for id, v := range l.values {
		values[id] = v
	}
	origins := make(map[string]string, len(l.valueOrigins))
	for id, v := range l.valueOrigins {
		origins[id] = v
	}
	resetOrigins := make(map[string]string, len(l.resetOrigins))
	for id, v := range l.resetOrigins {
		resetOrigins[id] = v
	}
	return State{
		Values:       values,
		Resets:       ids,
		Origins:      origins,
		ResetOrigins: resetOrigins,
	}
}

type loader struct {
	cat            *catalog.Catalog
	seen           map[string]bool
	rootHasVersion bool
	inherited      *layer
}

func (l *loader) loadFile(path string, root bool, depth int) (*layer, error) {
	if depth > 16 {
		return nil, fmt.Errorf("%s: include depth exceeds 16", path)
	}

	abs, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	if l.seen[abs] {
		return nil, fmt.Errorf("%s: file included more than once (include cycle?)", abs)
	}
	l.seen[abs] = true

	raw := make(map[string]any)
	if _, err := toml.DecodeFile(abs, &raw); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config not found: %s", abs)
		}
		return nil, fmt.Errorf("%s: %w", abs, err)
	}

	if rawVersion, ok := raw["config_version"]; ok {
		version, ok := rawVersion.(int64)
		if !ok || version != currentVersion {
			return nil, fmt.Errorf("%s: config_version must be %d", abs, currentVersion)
		}
		if root {
			l.rootHasVersion = true
		}
	}

	includes, err := stringList(raw["include"], "include")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	resetIDs, err := stringList(raw["reset"], "reset")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}

	local := newLayer()
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var problems []error
	for _, key := range keys {
		switch key {
		case "config_version", "include", "reset":
			continue
		}
		if err := flatten(key, raw[key], func(id string, v any) error {
			normalized, err := parseValue(l.cat, abs, id, v)
			if err != nil {
				return err
			}
			if existing, ok := local.values[id]; ok {
				if !value.Equal(existing, normalized) {
					return fmt.Errorf("%s: %s is set more than once with different values", abs, id)
				}
				return nil
			}
			local.values[id] = normalized
			local.valueOrigins[id] = abs
			return nil
		}); err != nil {
			problems = append(problems, err)
		}
	}

	for _, id := range resetIDs {
		if _, ok := l.cat.Get(id); !ok {
			problems = append(problems, fmt.Errorf("%s: unknown setting in reset: %q%s", abs, id, suggestion(l.cat, id)))
			continue
		}
		local.resets[id] = true
		local.resetOrigins[id] = abs
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	for id := range local.resets {
		if _, set := local.values[id]; set {
			return nil, fmt.Errorf("%s: %q is both set and reset in the same file", abs, id)
		}
	}

	acc := newLayer()
	for _, include := range includes {
		resolved := include
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(abs), resolved)
		}
		resolved = filepath.Clean(ExpandPath(resolved))
		child, err := l.loadFile(resolved, false, depth+1)
		if err != nil {
			return nil, err
		}
		acc.merge(child)
	}

	if root {
		l.inherited = acc.clone()
	}
	acc.merge(local)
	return acc, nil
}

func parseValue(cat *catalog.Catalog, path, id string, v any) (any, error) {
	setting, ok := cat.Get(id)
	if !ok {
		return nil, fmt.Errorf("%s: unknown setting %q%s", path, id, suggestion(cat, id))
	}
	if err := value.Check(v, setting.Type); err != nil {
		return nil, fmt.Errorf("%s: %s: %w", path, id, err)
	}

	normalized, err := value.Normalize(v, setting.Type)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", path, id, err)
	}
	if setting.Type == value.Enum {
		s := normalized.(string)
		if !slices.Contains(setting.Values, s) {
			return nil, fmt.Errorf("%s: %s: invalid value %q (want one of %s)", path, id, s, strings.Join(setting.Values, ", "))
		}
	}

	return normalized, nil
}

func flatten(prefix string, v any, fn func(id string, v any) error) error {
	if table, ok := v.(map[string]any); ok {
		keys := make([]string, 0, len(table))
		for key := range table {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := flatten(prefix+"."+key, table[key], fn); err != nil {
				return err
			}
		}
		return nil
	}
	return fn(prefix, v)
}

func stringList(v any, name string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss, nil
		}
		return nil, fmt.Errorf("%s must be a list of strings", name)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of strings", name)
		}
		if strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("%s cannot contain an empty string", name)
		}
		out = append(out, s)
	}
	return out, nil
}

func suggestion(cat *catalog.Catalog, id string) string {
	if s := cat.Suggest(id); s != "" {
		return fmt.Sprintf(" (did you mean %q?)", s)
	}
	return ""
}
