// Package tui implements the interactive config editor.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/metru/pear/internal/adapter"
	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
	"github.com/metru/pear/internal/value"
)

// Options configures the editor.
type Options struct {
	Catalog     *catalog.Catalog
	ConfigPath  string
	Only        []string
	AllowCreate bool
	Adapter     adapter.Adapter
	CanApply    bool
}

type statusKind int

const (
	statusSet statusKind = iota
	statusUnset
	statusReset
)

type currentValue struct {
	value any
	found bool
	known bool
	err   error
}

type row struct {
	setting catalog.Setting
	status  statusKind
	value   any
	origin  string
	current currentValue
	pending bool
}

// FilterValue implements list.Item.
func (r *row) FilterValue() string {
	return strings.Join([]string{
		r.setting.ID,
		r.setting.Description,
		r.setting.Domain,
		r.setting.Key,
		r.statusLabel(),
	}, " ")
}

func (r *row) statusLabel() string {
	switch r.status {
	case statusSet:
		return "set"
	case statusReset:
		return "reset"
	default:
		return "unset"
	}
}

type filterMode int

const (
	filterAll filterMode = iota
	filterConfigured
	filterUnset
	filterPending
)

func (f filterMode) String() string {
	switch f {
	case filterConfigured:
		return "configured"
	case filterUnset:
		return "unset"
	case filterPending:
		return "pending"
	default:
		return "all"
	}
}

func (f filterMode) next() filterMode {
	return (f + 1) % 4
}

type styles struct {
	title        lipgloss.Style
	subtitle     lipgloss.Style
	headerRule   lipgloss.Style
	footer       lipgloss.Style
	help         lipgloss.Style
	selected     lipgloss.Style
	selectedDesc lipgloss.Style
	normal       lipgloss.Style
	desc         lipgloss.Style
	set          lipgloss.Style
	unset        lipgloss.Style
	reset        lipgloss.Style
	pending      lipgloss.Style
	danger       lipgloss.Style
	box          lipgloss.Style
}

func newStyles(dark bool) styles {
	ld := lipgloss.LightDark(dark)
	purple := ld(lipgloss.Color("#5B21B6"), lipgloss.Color("#C4B5FD"))
	green := ld(lipgloss.Color("#166534"), lipgloss.Color("#86EFAC"))
	yellow := ld(lipgloss.Color("#854D0E"), lipgloss.Color("#FDE68A"))
	red := ld(lipgloss.Color("#991B1B"), lipgloss.Color("#FCA5A5"))
	gray := ld(lipgloss.Color("#6B7280"), lipgloss.Color("#9CA3AF"))
	dim := ld(lipgloss.Color("#9CA3AF"), lipgloss.Color("#6B7280"))
	return styles{
		title:        lipgloss.NewStyle().Bold(true).Foreground(purple),
		subtitle:     lipgloss.NewStyle().Foreground(gray),
		headerRule:   lipgloss.NewStyle().Foreground(dim).Faint(true),
		footer:       lipgloss.NewStyle().Foreground(gray),
		help:         lipgloss.NewStyle().Foreground(gray).Faint(true),
		selected:     lipgloss.NewStyle().Bold(true).Foreground(purple),
		selectedDesc: lipgloss.NewStyle().Foreground(gray),
		normal:       lipgloss.NewStyle(),
		desc:         lipgloss.NewStyle().Foreground(gray),
		set:          lipgloss.NewStyle().Foreground(green),
		unset:        lipgloss.NewStyle().Foreground(dim),
		reset:        lipgloss.NewStyle().Foreground(yellow),
		pending:      lipgloss.NewStyle().Foreground(yellow),
		danger:       lipgloss.NewStyle().Foreground(red),
		box:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(1, 2),
	}
}

func (s styles) status(kind statusKind) lipgloss.Style {
	switch kind {
	case statusSet:
		return s.set
	case statusReset:
		return s.reset
	default:
		return s.unset
	}
}

func statusIcon(kind statusKind) string {
	switch kind {
	case statusSet:
		return "●"
	case statusReset:
		return "↺"
	default:
		return "○"
	}
}

func compactHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~/" + strings.TrimPrefix(path, home+string(filepath.Separator))
	}
	return path
}

func displayValue(setting catalog.Setting, v any) string {
	if setting.Type == value.Path {
		if s, ok := v.(string); ok {
			return compactHome(s)
		}
	}
	return value.Display(v)
}

func displayCurrent(r *row) string {
	if r.current.err != nil {
		return "?"
	}
	if !r.current.known {
		return "—"
	}
	if !r.current.found {
		return "—"
	}
	return displayValue(r.setting, r.current.value)
}

func stateFromConfig(cfg *config.Config) config.State {
	s := config.State{
		Values:       cloneMap(cfg.Values),
		Resets:       append([]string(nil), cfg.Resets...),
		Origins:      cloneMap(cfg.Origins),
		ResetOrigins: cloneMap(cfg.ResetOrigins),
	}
	return s
}

func cloneState(s config.State) config.State {
	return config.State{
		Values:       cloneMap(s.Values),
		Resets:       append([]string(nil), s.Resets...),
		Origins:      cloneMap(s.Origins),
		ResetOrigins: cloneMap(s.ResetOrigins),
	}
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func emptyConfig(path string) *config.Config {
	return &config.Config{
		Path:         path,
		Version:      1,
		Values:       map[string]any{},
		Origins:      map[string]string{},
		ResetOrigins: map[string]string{},
		Inherited: config.State{
			Values:       map[string]any{},
			Origins:      map[string]string{},
			ResetOrigins: map[string]string{},
		},
	}
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func editCount(n int) string {
	if n == 1 {
		return "1 edit"
	}
	return fmt.Sprintf("%d edits", n)
}
