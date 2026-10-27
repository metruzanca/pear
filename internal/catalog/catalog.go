// Package catalog holds Pear's curated set of macOS settings.
//
// The catalog is data, not code: every supported setting is an entry in
// catalog.toml. Adding a setting should not require touching the engine.
package catalog

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed catalog.toml
var catalogData string

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	scopes      = map[string]bool{"user": true, "host": true, "system": true}
	adapters    = map[string]bool{"defaults": true}
	types       = map[string]bool{"bool": true, "int": true, "float": true, "string": true, "path": true, "enum": true, "string_list": true}
	risks       = map[string]bool{"safe": true, "sensitive": true, "critical": true}
	restartable = map[string]bool{"Dock": true, "Finder": true, "SystemUIServer": true, "ControlCenter": true}
)

// Setting describes one configurable macOS preference.
type Setting struct {
	ID          string   `toml:"id" json:"id"`
	Description string   `toml:"description" json:"description"`
	Scope       string   `toml:"scope" json:"scope"`
	Adapter     string   `toml:"adapter" json:"adapter"`
	Domain      string   `toml:"domain" json:"domain"`
	Key         string   `toml:"key" json:"key"`
	Type        string   `toml:"type" json:"type"`
	Values      []string `toml:"values,omitempty" json:"values,omitempty"`
	Restart     []string `toml:"restart,omitempty" json:"restart,omitempty"`
	Risk        string   `toml:"risk" json:"risk"`
}

// Catalog is an immutable, indexed set of settings.
type Catalog struct {
	settings []Setting
	byID     map[string]Setting
	byTarget map[string]Setting
}

// Load parses the embedded catalog.
func Load() (*Catalog, error) {
	return Parse(catalogData)
}

// Parse parses and validates a catalog document.
func Parse(data string) (*Catalog, error) {
	var doc struct {
		Settings []Setting `toml:"setting"`
	}

	md, err := toml.Decode(data, &doc)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("catalog: unknown field %q", undecoded[0].String())
	}

	c := &Catalog{
		settings: make([]Setting, 0, len(doc.Settings)),
		byID:     make(map[string]Setting, len(doc.Settings)),
		byTarget: make(map[string]Setting, len(doc.Settings)),
	}

	for i, s := range doc.Settings {
		if err := validateSetting(s); err != nil {
			return nil, fmt.Errorf("catalog: setting %d (%q): %w", i+1, s.ID, err)
		}
		if _, exists := c.byID[s.ID]; exists {
			return nil, fmt.Errorf("catalog: duplicate setting id %q", s.ID)
		}
		target := targetKey(s.Scope, s.Domain, s.Key)
		if other, exists := c.byTarget[target]; exists {
			return nil, fmt.Errorf("catalog: %q and %q write the same preference (%s %s %s)", other.ID, s.ID, s.Scope, s.Domain, s.Key)
		}
		c.settings = append(c.settings, s)
		c.byID[s.ID] = s
		c.byTarget[target] = s
	}

	sort.Slice(c.settings, func(i, j int) bool { return c.settings[i].ID < c.settings[j].ID })
	return c, nil
}

func validateSetting(s Setting) error {
	if !idPattern.MatchString(s.ID) {
		return fmt.Errorf("invalid id: use dotted lowercase names like %q", "dock.autohide")
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if !scopes[s.Scope] {
		return fmt.Errorf("invalid scope %q (want user, host, or system)", s.Scope)
	}
	if !adapters[s.Adapter] {
		return fmt.Errorf("invalid adapter %q", s.Adapter)
	}
	if s.Domain == "" {
		return fmt.Errorf("domain is required")
	}
	if s.Key == "" {
		return fmt.Errorf("key is required")
	}
	if !types[s.Type] {
		return fmt.Errorf("invalid type %q", s.Type)
	}
	if !risks[s.Risk] {
		return fmt.Errorf("invalid risk %q (want safe, sensitive, or critical)", s.Risk)
	}
	if s.Type == "enum" {
		if len(s.Values) == 0 {
			return fmt.Errorf("enum settings need at least one value")
		}
	} else if len(s.Values) > 0 {
		return fmt.Errorf("values is only valid for enum settings")
	}
	seen := make(map[string]bool, len(s.Values))
	for _, v := range s.Values {
		if v == "" {
			return fmt.Errorf("enum values cannot be empty")
		}
		if seen[v] {
			return fmt.Errorf("duplicate enum value %q", v)
		}
		seen[v] = true
	}
	for _, app := range s.Restart {
		if !restartable[app] {
			return fmt.Errorf("cannot restart %q", app)
		}
	}
	return nil
}

func targetKey(scope, domain, key string) string {
	return scope + "\x00" + domain + "\x00" + key
}

// Get returns a setting by id.
func (c *Catalog) Get(id string) (Setting, bool) {
	s, ok := c.byID[id]
	return s, ok
}

// All returns every setting in id order. Callers must not modify the result.
func (c *Catalog) All() []Setting {
	return c.settings
}

// Count returns the number of settings in the catalog.
func (c *Catalog) Count() int {
	return len(c.settings)
}

// IDs returns every setting id in order.
func (c *Catalog) IDs() []string {
	ids := make([]string, len(c.settings))
	for i, s := range c.settings {
		ids[i] = s.ID
	}
	return ids
}

// HasPrefix reports whether any setting id equals prefix or starts with prefix+".".
func (c *Catalog) HasPrefix(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return false
	}
	for _, s := range c.settings {
		if s.ID == prefix || strings.HasPrefix(s.ID, prefix+".") {
			return true
		}
	}
	return false
}

// Suggest returns the closest catalog id to id, or "" when nothing is close.
func (c *Catalog) Suggest(id string) string {
	best := ""
	bestDistance := 4
	for _, s := range c.settings {
		d := levenshtein(id, s.ID)
		if d < bestDistance {
			best, bestDistance = s.ID, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
