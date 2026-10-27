package tui

import (
	"charm.land/huh/v2"
)

type confirmAction int

const (
	confirmApply confirmAction = iota
	confirmQuit
	confirmReset
)

type confirmModel struct {
	action confirmAction
	id     string
	form   *huh.Form
	value  bool
	dark   bool
}

func newConfirm(action confirmAction, id, title, description, affirmative, negative string, defaultYes bool, width int, dark bool) *confirmModel {
	c := &confirmModel{action: action, id: id, value: defaultYes, dark: dark}
	field := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative(affirmative).
		Negative(negative).
		Value(&c.value)
	c.form = huh.NewForm(huh.NewGroup(field)).
		WithTheme(huh.ThemeFunc(huh.ThemeCharm)).
		WithWidth(max(width-4, 30)).
		WithKeyMap(noVimKeyMap()).
		WithShowHelp(false)
	return c
}

func (c *confirmModel) resize(width int) {
	if c.form == nil {
		return
	}
	c.form.WithWidth(max(width-4, 30))
}
