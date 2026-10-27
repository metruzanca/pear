package value

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckIsStrict(t *testing.T) {
	tests := []struct {
		name string
		v    any
		typ  string
		ok   bool
	}{
		{"bool", true, Bool, true},
		{"bool from int", int64(1), Bool, false},
		{"int", int64(44), Int, true},
		{"int from float", 44.0, Int, false},
		{"float from int", int64(1), Float, true},
		{"string", "scale", String, true},
		{"path", "~/Pictures", Path, true},
		{"empty path", "", Path, false},
		{"enum", "scale", Enum, true},
		{"string list", []any{"a", "b"}, StringList, true},
		{"string list with number", []any{"a", 1}, StringList, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Check(test.v, test.typ)
			if test.ok && err != nil {
				t.Fatalf("Check(%v, %s) = %v, want nil", test.v, test.typ, err)
			}
			if !test.ok && err == nil {
				t.Fatalf("Check(%v, %s) = nil, want error", test.v, test.typ)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		v    any
		typ  string
		want any
	}{
		{"uint to int64", uint64(44), Int, int64(44)},
		{"float int to int64", float64(44), Int, int64(44)},
		{"int to float64", int64(2), Float, float64(2)},
		{"strings to list", []any{"a", "b"}, StringList, []string{"a", "b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Normalize(test.v, test.typ)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if !Equal(got, test.want) {
				t.Fatalf("Normalize(%v) = %#v, want %#v", test.v, got, test.want)
			}
		})
	}

	if _, err := Normalize(44.5, Int); err == nil {
		t.Fatal("Normalize(44.5, int) = nil error, want error")
	}
}

func TestNormalizePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got, err := Normalize("~/Pictures/Screenshots", Path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Pictures/Screenshots")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := Normalize("relative/path", Path); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestEqual(t *testing.T) {
	if !Equal(0.25, 0.25+1e-12) {
		t.Fatal("float values within tolerance should be equal")
	}
	if Equal(0.25, 0.26) {
		t.Fatal("different floats should not be equal")
	}
	if !Equal([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("equal lists should compare equal")
	}
	if Equal(int64(1), 1.0) {
		t.Fatal("different canonical types should not compare equal")
	}
	if Equal(math.NaN(), math.NaN()) {
		t.Fatal("NaN should not compare equal")
	}
}

func TestDisplay(t *testing.T) {
	tests := map[string]any{
		"<unset>":    nil,
		"true":       true,
		"44":         int64(44),
		"0.25":       0.25,
		`"scale"`:    "scale",
		`["a", "b"]`: []string{"a", "b"},
	}
	for want, v := range tests {
		if got := Display(v); got != want {
			t.Errorf("Display(%#v) = %q, want %q", v, got, want)
		}
	}
}
