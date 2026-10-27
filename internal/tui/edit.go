package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/value"
)

type editModel struct {
	setting   catalog.Setting
	form      *huh.Form
	strValue  string
	boolValue bool
	listValue string
	dark      bool
}

func newEditModel(r *row, width int, dark bool) *editModel {
	e := &editModel{setting: r.setting, dark: dark}

	configText := "—"
	switch r.status {
	case statusSet:
		configText = displayValue(r.setting, r.value)
	case statusReset:
		configText = "reset"
	}
	description := fmt.Sprintf("%s\n\nconfig: %s\nmacOS: %s", r.setting.Description, configText, displayCurrent(r))

	var field huh.Field
	switch r.setting.Type {
	case value.Bool:
		if r.status == statusSet {
			if b, ok := r.value.(bool); ok {
				e.boolValue = b
			}
		} else if r.current.known && r.current.found {
			if b, ok := r.current.value.(bool); ok {
				e.boolValue = b
			}
		}
		field = huh.NewSelect[bool]().
			Title(r.setting.ID).
			Description(description).
			Options(huh.NewOption("true", true), huh.NewOption("false", false)).
			Value(&e.boolValue)
	case value.Enum:
		initial := r.setting.Values[0]
		if r.status == statusSet {
			if s, ok := r.value.(string); ok {
				initial = s
			}
		} else if r.current.known && r.current.found {
			if s, ok := r.current.value.(string); ok {
				initial = s
			}
		}
		e.strValue = initial
		options := make([]huh.Option[string], 0, len(r.setting.Values))
		for _, allowed := range r.setting.Values {
			options = append(options, huh.NewOption(allowed, allowed))
		}
		field = huh.NewSelect[string]().
			Title(r.setting.ID).
			Description(description).
			Options(options...).
			Value(&e.strValue)
	case value.Int, value.Float:
		e.strValue = initialScalar(r)
		field = huh.NewInput().
			Title(r.setting.ID).
			Description(description).
			Placeholder(placeholderFor(r.setting)).
			Value(&e.strValue).
			Validate(func(s string) error {
				_, err := parseScalar(r.setting, s)
				return err
			})
	case value.Path:
		e.strValue = compactHome(initialScalar(r))
		field = huh.NewInput().
			Title(r.setting.ID).
			Description(description).
			Placeholder("~/Pictures").
			Value(&e.strValue).
			Validate(func(s string) error {
				_, err := parseScalar(r.setting, s)
				return err
			})
	case value.StringList:
		if r.status == statusSet {
			if items, ok := r.value.([]string); ok {
				e.listValue = strings.Join(items, "\n")
			}
		}
		field = huh.NewText().
			Title(r.setting.ID).
			Description(description).
			Placeholder("one item per line").
			Lines(6).
			Value(&e.listValue).
			Validate(func(s string) error {
				_, err := parseScalar(r.setting, s)
				return err
			})
	default:
		e.strValue = initialScalar(r)
		field = huh.NewInput().
			Title(r.setting.ID).
			Description(description).
			Value(&e.strValue)
	}

	e.form = huh.NewForm(huh.NewGroup(field)).
		WithTheme(huh.ThemeFunc(huh.ThemeCharm)).
		WithWidth(max(width-4, 30)).
		WithKeyMap(noVimKeyMap()).
		WithShowHelp(true)
	return e
}

// Value returns the canonical value staged by the form.
func (e *editModel) Value() (any, error) {
	switch e.setting.Type {
	case value.Bool:
		return e.boolValue, nil
	case value.Enum:
		return parseScalar(e.setting, e.strValue)
	case value.StringList:
		return parseScalar(e.setting, e.listValue)
	case value.Path:
		raw, err := parseScalar(e.setting, e.strValue)
		if err != nil {
			return nil, err
		}
		return raw, nil
	default:
		return parseScalar(e.setting, e.strValue)
	}
}

func (e *editModel) resize(width int) {
	if e.form == nil {
		return
	}
	e.form.WithWidth(max(width-4, 30))
}

func initialScalar(r *row) string {
	if r.status == statusSet {
		switch v := r.value.(type) {
		case int64:
			return strconv.FormatInt(v, 10)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case string:
			return v
		}
	}
	if r.current.known && r.current.found {
		switch v := r.current.value.(type) {
		case int64:
			return strconv.FormatInt(v, 10)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case string:
			return v
		}
	}
	return ""
}

func placeholderFor(s catalog.Setting) string {
	switch s.Type {
	case value.Int:
		return "44"
	case value.Float:
		return "0.25"
	default:
		return ""
	}
}

func parseScalar(s catalog.Setting, raw string) (any, error) {
	switch s.Type {
	case value.Int:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("enter a whole number")
		}
		return n, nil
	case value.Float:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("enter a number")
		}
		return f, nil
	case value.Path:
		if err := value.Check(raw, value.Path); err != nil {
			return nil, err
		}
		if _, err := value.Normalize(raw, value.Path); err != nil {
			return nil, err
		}
		return raw, nil
	case value.StringList:
		lines := strings.Split(raw, "\n")
		items := make([]string, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				items = append(items, line)
			}
		}
		return items, nil
	case value.Enum:
		if err := value.Check(raw, value.Enum); err != nil {
			return nil, err
		}
		for _, allowed := range s.Values {
			if raw == allowed {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("choose one of %s", strings.Join(s.Values, ", "))
	default:
		if err := value.Check(raw, s.Type); err != nil {
			return nil, err
		}
		return raw, nil
	}
}

func noVimKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Select.Up.SetKeys("up")
	km.Select.Down.SetKeys("down")
	km.Select.HalfPageUp.SetKeys("pgup")
	km.Select.HalfPageDown.SetKeys("pgdown")
	km.Select.GotoTop.SetKeys("home")
	km.Select.GotoBottom.SetKeys("end")
	km.Select.Left.SetKeys("left")
	km.Select.Right.SetKeys("right")
	km.Text.NewLine.SetKeys("alt+enter")
	km.Confirm.Toggle.SetKeys("left", "right")
	return km
}
