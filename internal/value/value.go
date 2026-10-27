// Package value contains strict validation and canonicalisation for the
// scalar types Pear supports. Config values and values read back from macOS
// are both normalised through this package so comparisons are meaningful.
package value

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Type names used by the catalog.
const (
	Bool       = "bool"
	Int        = "int"
	Float      = "float"
	String     = "string"
	Path       = "path"
	Enum       = "enum"
	StringList = "string_list"
)

// Check reports whether v is a valid TOML/config value for typ. It is strict:
// a TOML integer is not accepted for a bool setting, even if it is 0 or 1.
func Check(v any, typ string) error {
	switch typ {
	case Bool:
		if _, ok := v.(bool); !ok {
			return typeError(v, "boolean")
		}
	case Int:
		switch v.(type) {
		case int64, int:
		default:
			return typeError(v, "integer")
		}
	case Float:
		// TOML decodes both whole and fractional numbers as float64 when the
		// target is an interface, but accept int for programmatic callers.
		switch v.(type) {
		case float64, int64, int:
		default:
			return typeError(v, "number")
		}
	case String, Enum:
		if _, ok := v.(string); !ok {
			return typeError(v, "string")
		}
	case Path:
		s, ok := v.(string)
		if !ok {
			return typeError(v, "path string")
		}
		if s == "" {
			return fmt.Errorf("path cannot be empty")
		}
	case StringList:
		items, ok := toStringSlice(v)
		if !ok {
			return typeError(v, "list of strings")
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("expected a list of strings, got %s", TypeName(v))
			}
		}
	default:
		return fmt.Errorf("unknown value type %q", typ)
	}
	return nil
}

// Normalize converts v into the canonical Go representation for typ:
// bool, int64, float64, string, or []string.
func Normalize(v any, typ string) (any, error) {
	switch typ {
	case Bool:
		switch x := v.(type) {
		case bool:
			return x, nil
		default:
			return nil, typeError(v, "boolean")
		}
	case Int:
		i, ok := toInt64(v)
		if !ok {
			return nil, typeError(v, "integer")
		}
		return i, nil
	case Float:
		f, ok := toFloat64(v)
		if !ok {
			return nil, typeError(v, "number")
		}
		return f, nil
	case String, Enum:
		s, ok := v.(string)
		if !ok {
			return nil, typeError(v, "string")
		}
		return s, nil
	case Path:
		s, ok := v.(string)
		if !ok {
			return nil, typeError(v, "path string")
		}
		normalized, err := normalizePath(s)
		if err != nil {
			return nil, err
		}
		return normalized, nil
	case StringList:
		items, ok := toStringSlice(v)
		if !ok {
			return nil, typeError(v, "list of strings")
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("expected a list of strings, got an element of type %s", TypeName(item))
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown value type %q", typ)
	}
}

// Equal compares two canonical values.
func Equal(a, b any) bool {
	switch x := a.(type) {
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		if !ok {
			return false
		}
		if math.IsNaN(x) || math.IsNaN(y) {
			return false
		}
		return math.Abs(x-y) <= 1e-9
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []string:
		y, ok := b.([]string)
		return ok && slices.Equal(x, y)
	default:
		return reflect.DeepEqual(a, b)
	}
}

// Display renders a canonical value for humans.
func Display(v any) string {
	if v == nil {
		return "<unset>"
	}
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return strconv.Quote(x)
	case []string:
		quoted := make([]string, len(x))
		for i, item := range x {
			quoted[i] = strconv.Quote(item)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// TypeName returns a human-friendly Go type name.
func TypeName(v any) string {
	if v == nil {
		return "unset"
	}
	switch v.(type) {
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64:
		return "integer"
	case uint, uint8, uint16, uint32, uint64:
		return "unsigned integer"
	case float32, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "list"
	case []string:
		return "list of strings"
	default:
		return reflect.TypeOf(v).String()
	}
}

func typeError(v any, want string) error {
	return fmt.Errorf("expected %s, got %s", want, TypeName(v))
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), uint64(x) <= math.MaxInt64
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), x <= math.MaxInt64
	case float64:
		if x != math.Trunc(x) || x < math.MinInt64 || x > math.MaxInt64 {
			return 0, false
		}
		return int64(x), true
	default:
		return 0, false
	}
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	default:
		return 0, false
	}
}

func toStringSlice(v any) ([]any, bool) {
	switch x := v.(type) {
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, true
	case []any:
		return x, true
	default:
		return nil, false
	}
}

func normalizePath(p string) (string, error) {
	switch {
	case p == "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		p = home
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute or start with ~: %q", p)
	}
	return filepath.Clean(p), nil
}
