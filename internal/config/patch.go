package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/value"
)

// EditKind describes one change to a config file.
type EditKind int

const (
	// SetValue writes or replaces a value.
	SetValue EditKind = iota
	// RemoveValue removes a value from the root file.
	RemoveValue
	// ResetSetting adds an id to the top-level reset list.
	ResetSetting
	// UnresetSetting removes an id from the top-level reset list.
	UnresetSetting
)

// Edit is a single config change.
type Edit struct {
	Kind  EditKind
	ID    string
	Value any
}

// ApplyEdits patches path, validates the result, and atomically replaces it.
// Comments and unrelated formatting are preserved. Edits are applied in order;
// setting a value removes an existing reset for that id and resetting removes
// an existing value.
func ApplyEdits(path string, cat *catalog.Catalog, edits []Edit) error {
	if len(edits) == 0 {
		return nil
	}
	if cat == nil {
		return fmt.Errorf("apply edits: catalog is required")
	}

	resolved, err := ResolvePath(path)
	if err != nil {
		return err
	}

	original, mode, err := readConfigFile(resolved)
	if err != nil {
		return err
	}

	doc := newDocument(string(original))
	initialResets, err := resetsIn(original)
	if err != nil {
		return fmt.Errorf("%s: %w", resolved, err)
	}

	valueOps := make(map[string]Edit)
	resetOps := make(map[string]bool)
	for _, edit := range edits {
		setting, ok := cat.Get(edit.ID)
		if !ok {
			return fmt.Errorf("edit %s: unknown setting%s", edit.ID, suggestion(cat, edit.ID))
		}
		switch edit.Kind {
		case SetValue:
			normalized, err := editValue(setting, edit.Value)
			if err != nil {
				return fmt.Errorf("edit %s: %w", edit.ID, err)
			}
			valueOps[edit.ID] = Edit{Kind: SetValue, ID: edit.ID, Value: normalized}
			resetOps[edit.ID] = false
		case RemoveValue:
			valueOps[edit.ID] = Edit{Kind: RemoveValue, ID: edit.ID}
		case ResetSetting:
			valueOps[edit.ID] = Edit{Kind: RemoveValue, ID: edit.ID}
			resetOps[edit.ID] = true
		case UnresetSetting:
			resetOps[edit.ID] = false
		default:
			return fmt.Errorf("edit %s: unknown edit kind %d", edit.ID, edit.Kind)
		}
	}

	ids := make([]string, 0, len(valueOps))
	for id := range valueOps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		setting, _ := cat.Get(id)
		op := valueOps[id]
		if op.Kind == SetValue {
			encoded, err := encodeTOMLValue(op.Value)
			if err != nil {
				return fmt.Errorf("edit %s: %w", id, err)
			}
			doc.setValue(setting, encoded)
		} else {
			doc.removeValue(setting)
		}
	}

	resets := make(map[string]bool, len(initialResets))
	for _, id := range initialResets {
		resets[id] = true
	}
	for id, add := range resetOps {
		if add {
			resets[id] = true
		} else {
			delete(resets, id)
		}
	}
	resetIDs := make([]string, 0, len(resets))
	for id := range resets {
		resetIDs = append(resetIDs, id)
	}
	sort.Strings(resetIDs)

	if !sameSet(initialResets, resetIDs) {
		if len(resetIDs) == 0 {
			doc.removeTopLevel("reset")
		} else {
			encoded, err := encodeTOMLValue(resetIDs)
			if err != nil {
				return fmt.Errorf("encode reset list: %w", err)
			}
			doc.setTopLevel("reset", encoded)
		}
	}

	updated := doc.String()
	if updated == string(original) {
		return nil
	}

	if err := validateAndReplace(resolved, []byte(updated), mode, cat); err != nil {
		return err
	}
	return nil
}

func readConfigFile(path string) ([]byte, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, 0, fmt.Errorf("read %s: %w", path, statErr)
		}
		return data, info.Mode().Perm(), nil
	}
	if !os.IsNotExist(err) {
		return nil, 0, fmt.Errorf("read %s: %w", path, err)
	}
	return []byte("config_version = 1\n"), 0o644, nil
}

func validateAndReplace(path string, data []byte, mode os.FileMode, cat *catalog.Catalog) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pear-config-*.toml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	if _, err := Load(tmpName, cat); err != nil {
		return fmt.Errorf("edited config is invalid: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func editValue(setting catalog.Setting, v any) (any, error) {
	if err := value.Check(v, setting.Type); err != nil {
		return nil, err
	}
	normalized, err := value.Normalize(v, setting.Type)
	if err != nil {
		return nil, err
	}
	if setting.Type == value.Enum {
		s := normalized.(string)
		ok := false
		for _, allowed := range setting.Values {
			if s == allowed {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("invalid value %q (want one of %s)", s, strings.Join(setting.Values, ", "))
		}
	}
	return normalized, nil
}

func resetsIn(data []byte) ([]string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, err
	}
	return stringList(raw["reset"], "reset")
}

func encodeTOMLValue(v any) (string, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"v": v}); err != nil {
		return "", err
	}
	s := strings.TrimSuffix(buf.String(), "\n")
	const prefix = "v = "
	if !strings.HasPrefix(s, prefix) {
		return "", fmt.Errorf("unexpected TOML encoding %q", s)
	}
	return strings.TrimPrefix(s, prefix), nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

// document is a line-oriented TOML file used for surgical edits.
type document struct {
	lines    []string
	crlf     bool
	trailing bool
}

func newDocument(s string) *document {
	crlf := strings.Contains(s, "\r\n")
	if crlf {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	trailing := strings.HasSuffix(s, "\n")
	body := strings.TrimSuffix(s, "\n")
	var lines []string
	if body == "" {
		lines = []string{""}
	} else {
		lines = strings.Split(body, "\n")
	}
	return &document{lines: lines, crlf: crlf, trailing: trailing}
}

func (d *document) String() string {
	s := strings.Join(d.lines, "\n")
	if d.trailing {
		s += "\n"
	}
	if d.crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

func (d *document) setValue(setting catalog.Setting, encoded string) {
	table, key := splitID(setting.ID)
	d.setKey(table, key, encoded)
}

func (d *document) removeValue(setting catalog.Setting) {
	table, key := splitID(setting.ID)
	d.removeKey(table, key)
}

func (d *document) setTopLevel(key, encoded string) {
	d.setKey("", key, encoded)
}

func (d *document) removeTopLevel(key string) {
	d.removeKey("", key)
}

func (d *document) setKey(table, key, encoded string) {
	if table != "" {
		if lineStart, lineEnd, indent, comment, found := d.findDottedKey(table, key); found {
			line := indent + table + "." + key + " = " + encoded
			if comment != "" {
				line += "  " + comment
			}
			d.replace(lineStart, lineEnd, []string{line})
			return
		}
	}

	start, end, ok := d.tableRange(table)
	if !ok {
		if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
			d.lines = append(d.lines, "")
		}
		d.lines = append(d.lines, "["+table+"]")
		start, end = len(d.lines), len(d.lines)
	}

	if lineStart, lineEnd, indent, comment, found := d.findKey(start, end, key); found {
		line := indent + key + " = " + encoded
		if comment != "" {
			line += "  " + comment
		}
		d.replace(lineStart, lineEnd, []string{line})
		return
	}

	pos := insertPosition(d.lines, start, end)
	line := key + " = " + encoded
	d.insert(pos, line)
}

func (d *document) removeKey(table, key string) {
	if table != "" {
		if lineStart, lineEnd, _, _, found := d.findDottedKey(table, key); found {
			d.replace(lineStart, lineEnd, nil)
			return
		}
	}
	start, end, ok := d.tableRange(table)
	if !ok {
		return
	}
	if lineStart, lineEnd, _, _, found := d.findKey(start, end, key); found {
		d.replace(lineStart, lineEnd, nil)
	}
}

func (d *document) tableRange(table string) (start, end int, ok bool) {
	if table == "" {
		end = len(d.lines)
		for i, line := range d.lines {
			if _, isHeader := headerTable(line); isHeader {
				end = i
				break
			}
		}
		return 0, end, true
	}
	for i, line := range d.lines {
		name, isHeader := headerTable(line)
		if !isHeader || name != table {
			continue
		}
		end = len(d.lines)
		for j := i + 1; j < len(d.lines); j++ {
			if _, next := headerTable(d.lines[j]); next {
				end = j
				break
			}
		}
		return i + 1, end, true
	}
	return 0, 0, false
}

func (d *document) findKey(start, end int, key string) (lineStart, lineEnd int, indent, comment string, ok bool) {
	return d.findAssignment(start, end, key)
}

func (d *document) findDottedKey(table, key string) (lineStart, lineEnd int, indent, comment string, ok bool) {
	_, end, _ := d.tableRange("")
	return d.findAssignment(0, end, table+"."+key)
}

func (d *document) findAssignment(start, end int, left string) (lineStart, lineEnd int, indent, comment string, ok bool) {
	for i := start; i < end; i++ {
		line := d.lines[i]
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			continue
		}
		if strings.TrimSpace(trimmed[:eq]) != left {
			continue
		}
		indent = line[:len(line)-len(trimmed)]
		endLine, trailingComment := scanValueSpan(d.lines, i, len(line)-len(trimmed)+eq+1)
		return i, endLine, indent, trailingComment, true
	}
	return 0, 0, "", "", false
}

func (d *document) replace(start, end int, replacement []string) {
	next := make([]string, 0, len(d.lines)-(end-start+1)+len(replacement))
	next = append(next, d.lines[:start]...)
	next = append(next, replacement...)
	next = append(next, d.lines[end+1:]...)
	d.lines = next
}

func (d *document) insert(pos int, lines ...string) {
	next := make([]string, 0, len(d.lines)+len(lines))
	next = append(next, d.lines[:pos]...)
	next = append(next, lines...)
	next = append(next, d.lines[pos:]...)
	d.lines = next
}

func insertPosition(lines []string, start, end int) int {
	if start > end {
		return start
	}
	pos := start
	for i := start; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			pos = i + 1
		}
	}
	return pos
}

func headerTable(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if i := strings.IndexByte(trimmed, '#'); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") || !strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" || strings.ContainsAny(inner, " \t\"'") {
		return "", false
	}
	return inner, true
}

func splitID(id string) (table, key string) {
	i := strings.LastIndexByte(id, '.')
	if i < 0 {
		return "", id
	}
	return id[:i], id[i+1:]
}

// scanValueSpan finds the last line of the value starting at line/col and
// returns any trailing comment on that line. It understands quoted strings,
// multi-line strings, arrays, inline tables, and comments.
func scanValueSpan(lines []string, line, col int) (endLine int, comment string) {
	var (
		square, curly                    int
		basic, literal                   bool
		multilineBasic, multilineLiteral bool
		escaped                          bool
		started                          bool
	)
	for li := line; li < len(lines); li++ {
		s := lines[li]
		start := 0
		if li == line {
			start = col
		}
		for ci := start; ci < len(s); ci++ {
			c := s[ci]

			if multilineBasic {
				if escaped {
					escaped = false
					continue
				}
				if c == '\\' {
					escaped = true
					continue
				}
				if strings.HasPrefix(s[ci:], `"""`) {
					multilineBasic = false
					ci += 2
				}
				continue
			}
			if multilineLiteral {
				if strings.HasPrefix(s[ci:], `'''`) {
					multilineLiteral = false
					ci += 2
				}
				continue
			}
			if basic {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					basic = false
				}
				continue
			}
			if literal {
				if c == '\'' {
					literal = false
				}
				continue
			}

			switch c {
			case ' ', '\t', '\r':
				continue
			case '#':
				if square == 0 && curly == 0 {
					return li, s[ci:]
				}
				ci = len(s)
			case '[':
				square++
				started = true
			case ']':
				square--
				started = true
			case '{':
				curly++
				started = true
			case '}':
				curly--
				started = true
			case '"':
				if strings.HasPrefix(s[ci:], `"""`) {
					multilineBasic = true
					ci += 2
				} else {
					basic = true
				}
				started = true
			case '\'':
				if strings.HasPrefix(s[ci:], `'''`) {
					multilineLiteral = true
					ci += 2
				} else {
					literal = true
				}
				started = true
			default:
				started = true
			}
		}

		if square == 0 && curly == 0 && !basic && !literal && !multilineBasic && !multilineLiteral && started {
			return li, ""
		}
	}
	return len(lines) - 1, ""
}
