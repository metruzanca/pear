// Package config loads and validates Pear's TOML configuration.
//
// Config keys are catalog ids: a table named dock with a key named autohide
// becomes "dock.autohide". Unknown keys are errors, not warnings, so a typo in
// a dotfiles repo fails loudly instead of silently doing nothing.
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

// Config is a fully resolved, validated configuration.
type Config struct {
	Path    string
	Version int
	Values  map[string]any // catalog id -> canonical value
	Resets  []string       // catalog ids to delete
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

// Load reads path, resolves includes, and validates every setting against cat.
func Load(path string, cat *catalog.Catalog) (*Config, error) {
	abs, err := filepath.Abs(ExpandPath(path))
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// Resolve a symlinked config to its real location so relative includes
	// resolve next to the file in a stow/chezmoi tree, not next to the link.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	l := &loader{
		cat:    cat,
		values: make(map[string]any),
		resets: make(map[string]bool),
		seen:   make(map[string]bool),
	}
	if err := l.loadFile(abs, true, 0); err != nil {
		return nil, err
	}
	if !l.rootHasVersion {
		return nil, fmt.Errorf("%s: missing required config_version = %d", abs, currentVersion)
	}

	ids := make([]string, 0, len(l.resets))
	for id := range l.resets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, set := l.values[id]; set {
			return nil, fmt.Errorf("%s: %q is both set and reset", abs, id)
		}
	}

	return &Config{
		Path:    abs,
		Version: currentVersion,
		Values:  l.values,
		Resets:  ids,
	}, nil
}

type loader struct {
	cat            *catalog.Catalog
	values         map[string]any
	resets         map[string]bool
	seen           map[string]bool
	rootHasVersion bool
}

func (l *loader) loadFile(path string, root bool, depth int) error {
	if depth > 16 {
		return fmt.Errorf("%s: include depth exceeds 16", path)
	}
	if l.seen[path] {
		return fmt.Errorf("%s: file included more than once (include cycle?)", path)
	}
	l.seen[path] = true

	raw := make(map[string]any)
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config not found: %s", path)
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	if rawVersion, ok := raw["config_version"]; ok {
		version, ok := rawVersion.(int64)
		if !ok || version != currentVersion {
			return fmt.Errorf("%s: config_version must be %d", path, currentVersion)
		}
		if root {
			l.rootHasVersion = true
		}
	}

	includes, err := stringList(raw["include"], "include")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	resets, err := stringList(raw["reset"], "reset")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	for _, include := range includes {
		resolved := include
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		resolved = filepath.Clean(ExpandPath(resolved))
		if err := l.loadFile(resolved, false, depth+1); err != nil {
			return err
		}
	}

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	local := make(map[string]any)
	var problems []error
	for _, key := range keys {
		switch key {
		case "config_version", "include", "reset":
			continue
		}
		if err := flatten(key, raw[key], func(id string, v any) error {
			normalized, err := l.parseValue(path, id, v)
			if err != nil {
				return err
			}
			if existing, ok := local[id]; ok {
				if !value.Equal(existing, normalized) {
					return fmt.Errorf("%s: %s is set more than once with different values", path, id)
				}
				return nil
			}
			local[id] = normalized
			return nil
		}); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}

	// Values in this file override values from its includes.
	for id, v := range local {
		l.values[id] = v
	}

	for _, id := range resets {
		if _, ok := l.cat.Get(id); !ok {
			return fmt.Errorf("%s: unknown setting in reset: %q%s", path, id, suggestion(l.cat, id))
		}
		l.resets[id] = true
	}
	return nil
}

func (l *loader) parseValue(path, id string, v any) (any, error) {
	setting, ok := l.cat.Get(id)
	if !ok {
		return nil, fmt.Errorf("%s: unknown setting %q%s", path, id, suggestion(l.cat, id))
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
