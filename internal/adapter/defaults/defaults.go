// Package defaults implements the adapter for preferences stored via macOS's
// defaults(1) tool.
package defaults

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"howett.net/plist"

	"github.com/metru/pear/internal/adapter"
	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/value"
)

// Runner executes a command. Tests substitute a fake runner.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner runs real processes.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Adapter reads and writes preferences with defaults(1).
type Adapter struct {
	Defaults string
	Killall  string
	Runner   Runner

	mu    sync.Mutex
	cache map[string]*domain
}

type domain struct {
	values map[string]any
	found  bool
}

// New returns an adapter using the system defaults and killall binaries.
func New() *Adapter {
	return &Adapter{
		Defaults: "/usr/bin/defaults",
		Killall:  "/usr/bin/killall",
		Runner:   ExecRunner{},
		cache:    make(map[string]*domain),
	}
}

var _ adapter.Adapter = (*Adapter)(nil)

// Supports implements adapter.Adapter.
func (a *Adapter) Supports(scope string) bool {
	return scope == "user" || scope == "host"
}

// Refresh clears cached domains so the next reads see external changes.
func (a *Adapter) Refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = make(map[string]*domain)
}

// Read implements adapter.Adapter.
func (a *Adapter) Read(ctx context.Context, setting catalog.Setting) (any, bool, error) {
	if !a.Supports(setting.Scope) {
		return nil, false, fmt.Errorf("defaults adapter does not support %s scope", setting.Scope)
	}
	dom, err := a.readDomain(ctx, setting.Scope, setting.Domain)
	if err != nil {
		return nil, false, err
	}
	if !dom.found {
		return nil, false, nil
	}
	raw, ok := dom.values[setting.Key]
	if !ok {
		return nil, false, nil
	}
	normalized, err := value.Normalize(raw, setting.Type)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: stored value has unexpected type: %w", setting.ID, err)
	}
	return normalized, true, nil
}

// Write implements adapter.Adapter.
func (a *Adapter) Write(ctx context.Context, setting catalog.Setting, v any) error {
	if !a.Supports(setting.Scope) {
		return fmt.Errorf("defaults adapter does not support %s scope", setting.Scope)
	}
	args := a.commandArgs(setting)
	args = append(args, "write", setting.Domain, setting.Key)

	switch setting.Type {
	case value.Bool:
		args = append(args, "-bool", strconv.FormatBool(v.(bool)))
	case value.Int:
		args = append(args, "-int", strconv.FormatInt(v.(int64), 10))
	case value.Float:
		args = append(args, "-float", strconv.FormatFloat(v.(float64), 'f', -1, 64))
	case value.String, value.Enum, value.Path:
		args = append(args, "-string", v.(string))
	case value.StringList:
		items := v.([]string)
		args = append(args, "-array")
		args = append(args, items...)
	default:
		return fmt.Errorf("write %s: unsupported type %q", setting.ID, setting.Type)
	}

	if _, stderr, err := a.runner().Run(ctx, a.defaultsPath(), args...); err != nil {
		return fmt.Errorf("write %s: %w: %s", setting.ID, err, strings.TrimSpace(string(stderr)))
	}
	a.invalidate(setting.Scope, setting.Domain)
	return nil
}

// Delete implements adapter.Adapter.
func (a *Adapter) Delete(ctx context.Context, setting catalog.Setting) error {
	if !a.Supports(setting.Scope) {
		return fmt.Errorf("defaults adapter does not support %s scope", setting.Scope)
	}
	args := a.commandArgs(setting)
	args = append(args, "delete", setting.Domain, setting.Key)

	_, stderr, err := a.runner().Run(ctx, a.defaultsPath(), args...)
	if err != nil {
		// Deleting a missing key or a missing domain is already the desired
		// state. defaults(1) uses a few different messages for this.
		msg := strings.ToLower(string(stderr))
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "not found") {
			a.invalidate(setting.Scope, setting.Domain)
			return nil
		}
		return fmt.Errorf("delete %s: %w: %s", setting.ID, err, strings.TrimSpace(string(stderr)))
	}
	a.invalidate(setting.Scope, setting.Domain)
	return nil
}

// Restart implements adapter.Adapter.
func (a *Adapter) Restart(ctx context.Context, target string) error {
	killall := a.Killall
	if killall == "" {
		killall = "/usr/bin/killall"
	}
	_, stderr, err := a.runner().Run(ctx, killall, target)
	if err != nil {
		// macOS's killall exits 1 when the app is not running. That is fine:
		// the preferences are still written, and the next launch reads them.
		msg := strings.ToLower(string(stderr))
		if strings.Contains(msg, "no matching processes") {
			return nil
		}
		return fmt.Errorf("restart %s: %w: %s", target, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (a *Adapter) readDomain(ctx context.Context, scope, name string) (*domain, error) {
	cacheKey := scope + "\x00" + name

	a.mu.Lock()
	if cached, ok := a.cache[cacheKey]; ok {
		a.mu.Unlock()
		return cached, nil
	}
	a.mu.Unlock()

	args := []string{}
	if scope == "host" {
		args = append(args, "-currentHost")
	}
	args = append(args, "export", name, "-")

	stdout, stderr, err := a.runner().Run(ctx, a.defaultsPath(), args...)
	if err != nil {
		if isMissingDomain(stderr) {
			dom := &domain{values: map[string]any{}, found: false}
			a.store(cacheKey, dom)
			return dom, nil
		}
		return nil, fmt.Errorf("read domain %s: %w: %s", name, err, strings.TrimSpace(string(stderr)))
	}

	values := make(map[string]any)
	if len(bytes.TrimSpace(stdout)) > 0 {
		if _, err := plist.Unmarshal(stdout, &values); err != nil {
			return nil, fmt.Errorf("read domain %s: parse plist: %w", name, err)
		}
	}
	dom := &domain{values: values, found: true}
	a.store(cacheKey, dom)
	return dom, nil
}

func isMissingDomain(stderr []byte) bool {
	msg := strings.ToLower(string(stderr))
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "could not be found") ||
		strings.Contains(msg, "not found")
}

func (a *Adapter) store(key string, dom *domain) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil {
		a.cache = make(map[string]*domain)
	}
	a.cache[key] = dom
}

func (a *Adapter) invalidate(scope, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cache, scope+"\x00"+name)
}

func (a *Adapter) commandArgs(setting catalog.Setting) []string {
	args := []string{}
	if setting.Scope == "host" {
		args = append(args, "-currentHost")
	}
	return args
}

func (a *Adapter) defaultsPath() string {
	if a.Defaults != "" {
		return a.Defaults
	}
	return "/usr/bin/defaults"
}

func (a *Adapter) runner() Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return ExecRunner{}
}
