package tui

import (
	"fmt"
	"io"
	"path/filepath"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type delegate struct {
	styles      styles
	configPath  string
	showCurrent bool
}

func (d delegate) Height() int  { return 2 }
func (d delegate) Spacing() int { return 1 }

func (d delegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d delegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	r, ok := item.(*row)
	if !ok {
		return
	}
	selected := index == m.Index()
	width := m.Width()
	if width < 20 {
		width = 20
	}

	marker := "  "
	if selected {
		marker = "▸ "
	}

	configText := "—"
	switch r.status {
	case statusSet:
		configText = displayValue(r.setting, r.value)
	case statusReset:
		configText = "reset"
	}

	idStyle := d.styles.normal
	if selected {
		idStyle = d.styles.selected
	}
	line1 := marker +
		d.styles.status(r.status).Render(statusIcon(r.status)) + " " +
		idStyle.Render(fmt.Sprintf("%-27s", r.setting.ID)) + " " +
		d.styles.subtitle.Render(fmt.Sprintf("%-5s", r.setting.Type)) +
		" config: " + configText

	meta := ""
	if r.origin != "" && !samePath(r.origin, d.configPath) {
		meta += " · from " + filepath.Base(r.origin)
	}
	if d.showCurrent {
		meta += " · macOS: " + displayCurrent(r)
	}
	if r.pending {
		meta += " · pending apply"
	}
	line2 := "  " + r.setting.Description + meta

	fmt.Fprint(w, ansi.Truncate(line1, width, "…")+"\n"+d.styles.desc.Render(ansi.Truncate(line2, width, "…")))
}
