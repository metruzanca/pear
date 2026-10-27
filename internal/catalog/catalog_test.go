package catalog

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	cat, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cat.Count() < 25 {
		t.Fatalf("catalog has %d settings, want at least 25", cat.Count())
	}
	if _, ok := cat.Get("dock.autohide"); !ok {
		t.Fatal("dock.autohide missing")
	}
	if _, ok := cat.Get("screenshots.location"); !ok {
		t.Fatal("screenshots.location missing")
	}
	// All returns sorted ids.
	ids := cat.IDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("catalog is not sorted: %q before %q", ids[i-1], ids[i])
		}
	}
}

func TestSuggest(t *testing.T) {
	cat, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Suggest("dock.autohid"); got != "dock.autohide" {
		t.Fatalf("Suggest = %q, want dock.autohide", got)
	}
	if got := cat.Suggest("finer.show_hidden"); got != "finder.show_hidden" {
		t.Fatalf("Suggest = %q, want finder.show_hidden", got)
	}
	if got := cat.Suggest("totally.unrelated"); got != "" {
		t.Fatalf("Suggest = %q, want empty", got)
	}
}

func TestHasPrefix(t *testing.T) {
	cat, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cat.HasPrefix("dock") {
		t.Fatal("dock prefix not found")
	}
	if !cat.HasPrefix("dock.autohide") {
		t.Fatal("exact id prefix not found")
	}
	if cat.HasPrefix("dockx") {
		t.Fatal("dockx should not match")
	}
}

func TestParseRejectsInvalidCatalogs(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "duplicate id",
			data: `
[[setting]]
id = "a.b"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k"
type = "bool"
risk = "safe"

[[setting]]
id = "a.b"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k2"
type = "bool"
risk = "safe"
`,
			want: "duplicate setting id",
		},
		{
			name: "duplicate target",
			data: `
[[setting]]
id = "a.b"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k"
type = "bool"
risk = "safe"

[[setting]]
id = "a.c"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k"
type = "bool"
risk = "safe"
`,
			want: "write the same preference",
		},
		{
			name: "enum without values",
			data: `
[[setting]]
id = "a.b"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k"
type = "enum"
risk = "safe"
`,
			want: "enum settings need",
		},
		{
			name: "bad restart target",
			data: `
[[setting]]
id = "a.b"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k"
type = "bool"
restart = ["Everything"]
risk = "safe"
`,
			want: "cannot restart",
		},
		{
			name: "unknown field",
			data: `
[[setting]]
id = "a.b"
description = "x"
scope = "user"
adapter = "defaults"
domain = "d"
key = "k"
type = "bool"
risk = "safe"
colour = "blue"
`,
			want: "unknown field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(test.data)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not contain %q", err, test.want)
			}
		})
	}
}
