// Package cli implements Pear's command line interface.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/metru/pear/internal/adapter/defaults"
	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
	"github.com/metru/pear/internal/engine"
	"github.com/metru/pear/internal/state"
	"github.com/metru/pear/internal/value"
)

// Version is set at build time with -ldflags.
var Version = "0.1.0-dev"

const usage = `pear - declarative macOS settings from your dotfiles

usage:
  pear apply [flags]        apply the config (writes changed settings)
  pear plan [flags]         show pending changes without writing
  pear validate [flags]     parse and validate the config
  pear list [flags]         list the catalog
  pear explain <setting>    explain one setting
  pear doctor [flags]       check the config, catalog, and environment
  pear snapshots            list saved snapshots
  pear restore [snapshot]   restore a previous apply
  pear version              print the version

flags (most commands):
  --config PATH   config file (default: $PEAR_CONFIG or ~/.config/pear/config.toml)
  --only PREFIX   limit to an id or group, comma separated (e.g. dock,finder)

apply/plan flags:
  --dry-run       plan only (apply)
  --json          machine-readable output
`

type command struct {
	stdout io.Writer
	stderr io.Writer
	cat    *catalog.Catalog
}

// Run executes a command and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	cmd := &command{stdout: stdout, stderr: stderr}

	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintf(stdout, "pear %s\n", Version)
		return 0
	case "apply":
		return cmd.apply(args[1:])
	case "plan":
		return cmd.plan(args[1:])
	case "validate":
		return cmd.validate(args[1:])
	case "list":
		return cmd.list(args[1:])
	case "explain":
		return cmd.explain(args[1:])
	case "doctor":
		return cmd.doctor(args[1:])
	case "snapshots":
		return cmd.snapshots(args[1:])
	case "restore":
		return cmd.restore(args[1:])
	default:
		fmt.Fprintf(stderr, "pear: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func (c *command) loadCatalog() bool {
	if c.cat != nil {
		return true
	}
	cat, err := catalog.Load()
	if err != nil {
		c.errorf("%v", err)
		return false
	}
	c.cat = cat
	return true
}

func (c *command) loadConfig(path string) (*config.Config, bool) {
	if !c.loadCatalog() {
		return nil, false
	}
	cfg, err := config.Load(resolveConfig(path), c.cat)
	if err != nil {
		c.errorf("%v", err)
		return nil, false
	}
	return cfg, true
}

func (c *command) apply(args []string) int {
	fs := newFlagSet("apply", c.stderr)
	path := fs.String("config", "", "config file")
	only := fs.String("only", "", "limit to id or group")
	dryRun := fs.Bool("dry-run", false, "plan only")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 0 {
		c.errorf("apply: unexpected argument %q", fs.Arg(0))
		return 2
	}
	if !requireDarwin(c.stderr) {
		return 2
	}
	cfg, ok := c.loadConfig(*path)
	if !ok {
		return 2
	}

	eng := &engine.Engine{
		Catalog: c.cat,
		Adapter: defaults.New(),
	}
	if !*dryRun {
		store, err := state.Default()
		if err != nil {
			c.errorf("%v", err)
			return 2
		}
		eng.Store = store
	}

	ctx := context.Background()
	plan, err := eng.BuildPlan(ctx, cfg, splitOnly(*only))
	if err != nil {
		c.errorf("%v", err)
		return 2
	}

	if *dryRun {
		if *asJSON {
			c.writeJSON(applyOutput{
				DryRun:    true,
				Plan:      makePlanOutput(plan),
				Unchanged: plan.Unchanged,
				Skipped:   plan.Skipped,
			})
			return 0
		}
		fmt.Fprintln(c.stdout, "Dry run. No changes written.")
		c.printPlan(plan)
		return 0
	}

	if !*asJSON {
		c.printPlan(plan)
	}

	if !plan.HasChanges() {
		if *asJSON {
			c.writeJSON(applyOutput{
				Plan:      makePlanOutput(plan),
				Unchanged: plan.Unchanged,
			})
			return 0
		}
		fmt.Fprintf(c.stdout, "Already converged: %d setting(s) match.\n", plan.Unchanged)
		return 0
	}

	if !*asJSON {
		fmt.Fprintln(c.stdout, "Applying...")
	}

	result, err := eng.Apply(ctx, plan)
	if err != nil {
		c.errorf("%v", err)
		return 2
	}

	if *asJSON {
		c.writeJSON(applyOutput{
			Plan:      makePlanOutput(plan),
			Applied:   result.Applied,
			Unchanged: result.Unchanged,
			Skipped:   result.Skipped,
			Restarts:  result.Restarts,
			Snapshot:  result.SnapshotDir,
			Failures:  makeFailureOutput(result.Failures),
		})
	} else {
		c.printResult(result)
	}

	if len(result.Failures) > 0 {
		return 2
	}
	return 0
}

func (c *command) plan(args []string) int {
	fs := newFlagSet("plan", c.stderr)
	path := fs.String("config", "", "config file")
	only := fs.String("only", "", "limit to id or group")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 0 {
		c.errorf("plan: unexpected argument %q", fs.Arg(0))
		return 2
	}
	if !requireDarwin(c.stderr) {
		return 2
	}
	cfg, ok := c.loadConfig(*path)
	if !ok {
		return 2
	}

	eng := &engine.Engine{Catalog: c.cat, Adapter: defaults.New()}
	plan, err := eng.BuildPlan(context.Background(), cfg, splitOnly(*only))
	if err != nil {
		c.errorf("%v", err)
		return 2
	}

	if *asJSON {
		c.writeJSON(makePlanOutput(plan))
	} else {
		c.printPlan(plan)
		if plan.HasChanges() {
			fmt.Fprintln(c.stdout, "\nRun `pear apply` to apply.")
		}
	}

	if plan.HasChanges() {
		return 1
	}
	return 0
}

func (c *command) validate(args []string) int {
	fs := newFlagSet("validate", c.stderr)
	path := fs.String("config", "", "config file")
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 0 {
		c.errorf("validate: unexpected argument %q", fs.Arg(0))
		return 2
	}
	cfg, ok := c.loadConfig(*path)
	if !ok {
		return 2
	}
	fmt.Fprintf(c.stdout, "Config OK: %s\n", cfg.Path)
	fmt.Fprintf(c.stdout, "  %d setting(s), %d reset(s)\n", len(cfg.Values), len(cfg.Resets))
	return 0
}

func (c *command) list(args []string) int {
	fs := newFlagSet("list", c.stderr)
	asJSON := fs.Bool("json", false, "machine-readable output")
	scope := fs.String("scope", "", "filter by scope")
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 0 {
		c.errorf("list: unexpected argument %q", fs.Arg(0))
		return 2
	}
	if !c.loadCatalog() {
		return 2
	}

	all := c.cat.All()
	settings := make([]catalog.Setting, 0, len(all))
	settings = append(settings, all...)
	if *scope != "" {
		filtered := settings[:0]
		for _, setting := range settings {
			if setting.Scope == *scope {
				filtered = append(filtered, setting)
			}
		}
		settings = filtered
	}

	if *asJSON {
		c.writeJSON(settings)
		return 0
	}

	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tSCOPE\tDESCRIPTION")
	for _, setting := range settings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", setting.ID, setting.Type, setting.Scope, setting.Description)
	}
	tw.Flush()
	return 0
}

func (c *command) explain(args []string) int {
	fs := newFlagSet("explain", c.stderr)
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(c.stderr, "usage: pear explain <setting>")
		return 2
	}
	if !c.loadCatalog() {
		return 2
	}

	id := fs.Arg(0)
	setting, ok := c.cat.Get(id)
	if !ok {
		suggestion := ""
		if s := c.cat.Suggest(id); s != "" {
			suggestion = fmt.Sprintf(" (did you mean %q?)", s)
		}
		c.errorf("unknown setting %q%s", id, suggestion)
		return 2
	}

	fmt.Fprintf(c.stdout, "%s\n  %s\n\n", setting.ID, setting.Description)
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  scope\t%s\n", setting.Scope)
	fmt.Fprintf(tw, "  domain\t%s\n", setting.Domain)
	fmt.Fprintf(tw, "  key\t%s\n", setting.Key)
	fmt.Fprintf(tw, "  type\t%s\n", setting.Type)
	if len(setting.Values) > 0 {
		fmt.Fprintf(tw, "  values\t%s\n", strings.Join(setting.Values, ", "))
	}
	fmt.Fprintf(tw, "  risk\t%s\n", setting.Risk)
	if len(setting.Restart) > 0 {
		fmt.Fprintf(tw, "  restart\t%s\n", strings.Join(setting.Restart, ", "))
	}
	tw.Flush()

	if runtime.GOOS != "darwin" {
		return 0
	}
	adapter := defaults.New()
	if !adapter.Supports(setting.Scope) {
		fmt.Fprintf(c.stdout, "\n  current\t(unavailable in this build)\n")
		return 0
	}
	current, found, err := adapter.Read(context.Background(), setting)
	if err != nil {
		fmt.Fprintf(c.stdout, "\n  current\t(unreadable: %v)\n", err)
		return 0
	}
	if !found {
		fmt.Fprintf(c.stdout, "\n  current\t<unset>\n")
	} else {
		fmt.Fprintf(c.stdout, "\n  current\t%s\n", value.Display(current))
	}
	return 0
}

func (c *command) doctor(args []string) int {
	fs := newFlagSet("doctor", c.stderr)
	path := fs.String("config", "", "config file")
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 0 {
		c.errorf("doctor: unexpected argument %q", fs.Arg(0))
		return 2
	}
	if !c.loadCatalog() {
		return 2
	}

	problems := 0
	line := func(label, format string, args ...any) {
		fmt.Fprintf(c.stdout, "  %-10s %s\n", label, fmt.Sprintf(format, args...))
	}

	fmt.Fprintln(c.stdout, "pear doctor")

	osName := runtime.GOOS
	if runtime.GOOS == "darwin" {
		version := "unknown"
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		}
		osName = fmt.Sprintf("darwin %s", version)
		line("os", "%s %s", osName, runtime.GOARCH)
		line("apply", "available")
	} else {
		line("os", "%s %s", osName, runtime.GOARCH)
		line("apply", "unavailable (pear apply requires macOS; validate still works)")
	}

	line("catalog", "%d setting(s)", c.cat.Count())

	cfgPath := resolveConfig(*path)
	if _, err := os.Stat(cfgPath); err != nil {
		line("config", "%s (missing)", cfgPath)
		problems++
	} else if cfg, err := config.Load(cfgPath, c.cat); err != nil {
		line("config", "%s (invalid)", cfgPath)
		fmt.Fprintf(c.stderr, "pear: %v\n", err)
		problems++
	} else {
		line("config", "%s (valid: %d setting(s), %d reset(s))", cfg.Path, len(cfg.Values), len(cfg.Resets))
	}

	if runtime.GOOS == "darwin" {
		for _, binary := range []string{"defaults", "killall"} {
			if p, err := exec.LookPath(binary); err == nil {
				line(binary, "%s", p)
			} else {
				line(binary, "missing")
				problems++
			}
		}
	}

	stateDir := state.DefaultDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		line("state", "%s (unwritable)", stateDir)
		problems++
	} else if file, err := os.CreateTemp(stateDir, ".doctor-*"); err != nil {
		line("state", "%s (unwritable)", stateDir)
		problems++
	} else {
		file.Close()
		os.Remove(file.Name())
		line("state", "%s (writable)", stateDir)
	}

	if problems > 0 {
		return 1
	}
	return 0
}

func (c *command) snapshots(args []string) int {
	fs := newFlagSet("snapshots", c.stderr)
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 0 {
		c.errorf("snapshots: unexpected argument %q", fs.Arg(0))
		return 2
	}
	store, err := state.Default()
	if err != nil {
		c.errorf("%v", err)
		return 2
	}
	infos, err := store.List()
	if err != nil {
		c.errorf("%v", err)
		return 2
	}
	if len(infos) == 0 {
		fmt.Fprintln(c.stdout, "No snapshots.")
		return 0
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCREATED\tENTRIES")
	for _, info := range infos {
		fmt.Fprintf(tw, "%s\t%s\t%d\n", info.Name, info.CreatedAt.Local().Format("2006-01-02 15:04:05"), info.Entries)
	}
	tw.Flush()
	return 0
}

func (c *command) restore(args []string) int {
	fs := newFlagSet("restore", c.stderr)
	only := fs.String("only", "", "limit to id or group")
	if !parseFlags(fs, args, c.stderr) {
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(c.stderr, "usage: pear restore [snapshot]")
		return 2
	}
	if !requireDarwin(c.stderr) {
		return 2
	}
	if !c.loadCatalog() {
		return 2
	}
	store, err := state.Default()
	if err != nil {
		c.errorf("%v", err)
		return 2
	}
	name := ""
	if fs.NArg() == 1 {
		name = fs.Arg(0)
	}
	snap, path, err := store.Open(name)
	if err != nil {
		c.errorf("%v", err)
		return 2
	}

	eng := &engine.Engine{Catalog: c.cat, Adapter: defaults.New(), Store: store}
	result, err := eng.Restore(context.Background(), snap, splitOnly(*only))
	if err != nil {
		c.errorf("%v", err)
		return 2
	}

	fmt.Fprintf(c.stdout, "Restored %d setting(s) from %s\n", result.Applied, path)
	if len(result.Restarts) > 0 {
		fmt.Fprintf(c.stdout, "Restarted %s.\n", strings.Join(result.Restarts, ", "))
	}
	if len(result.Failures) > 0 {
		for _, failure := range result.Failures {
			fmt.Fprintf(c.stderr, "pear: %s\n", failure)
		}
		return 2
	}
	return 0
}

func (c *command) printPlan(plan *engine.Plan) {
	fmt.Fprintf(c.stdout, "Plan: %d change(s), %d unchanged", len(plan.Changes), plan.Unchanged)
	if plan.Skipped > 0 {
		fmt.Fprintf(c.stdout, ", %d skipped", plan.Skipped)
	}
	fmt.Fprintln(c.stdout)
	if !plan.HasChanges() {
		return
	}

	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	for _, change := range plan.Changes {
		desired := "<unset>"
		if !change.Reset {
			desired = value.Display(change.Desired)
		}
		note := ""
		if len(change.Setting.Restart) > 0 {
			note = "restart " + strings.Join(change.Setting.Restart, ", ")
		}
		if change.Setting.Risk != "safe" {
			if note != "" {
				note += "; "
			}
			note += change.Setting.Risk
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s -> %s\t%s\n",
			strings.ToUpper(change.Action()), change.Setting.ID, value.Display(change.Current), desired, note)
	}
	tw.Flush()
}

func (c *command) printResult(result *engine.ApplyResult) {
	fmt.Fprintf(c.stdout, "Applied %d change(s), %d unchanged", result.Applied, result.Unchanged)
	if result.Skipped > 0 {
		fmt.Fprintf(c.stdout, ", %d skipped", result.Skipped)
	}
	if len(result.Failures) > 0 {
		fmt.Fprintf(c.stdout, ", %d failure(s)", len(result.Failures))
	}
	fmt.Fprintln(c.stdout)

	for _, target := range result.Restarts {
		fmt.Fprintf(c.stdout, "Restarted %s.\n", target)
	}
	if result.SnapshotDir != "" {
		fmt.Fprintf(c.stdout, "Snapshot: %s\n", result.SnapshotDir)
	}
	for _, failure := range result.Failures {
		fmt.Fprintf(c.stderr, "pear: %s\n", failure)
	}
}

func (c *command) errorf(format string, args ...any) {
	fmt.Fprintf(c.stderr, "pear: "+format+"\n", args...)
}

func (c *command) writeJSON(v any) {
	encoder := json.NewEncoder(c.stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(v)
}

type changeOutput struct {
	ID      string   `json:"id"`
	Action  string   `json:"action"`
	Current any      `json:"current"`
	Desired any      `json:"desired"`
	Existed bool     `json:"existed"`
	Restart []string `json:"restart,omitempty"`
	Risk    string   `json:"risk"`
}

type planOutput struct {
	Changes   []changeOutput `json:"changes"`
	Unchanged int            `json:"unchanged"`
	Skipped   int            `json:"skipped"`
}

type failureOutput struct {
	ID    string `json:"id,omitempty"`
	Stage string `json:"stage"`
	Error string `json:"error"`
}

type applyOutput struct {
	DryRun    bool            `json:"dry_run"`
	Plan      planOutput      `json:"plan"`
	Applied   int             `json:"applied"`
	Unchanged int             `json:"unchanged"`
	Skipped   int             `json:"skipped"`
	Restarts  []string        `json:"restarts,omitempty"`
	Snapshot  string          `json:"snapshot,omitempty"`
	Failures  []failureOutput `json:"failures,omitempty"`
}

func makePlanOutput(plan *engine.Plan) planOutput {
	out := planOutput{
		Changes:   make([]changeOutput, 0, len(plan.Changes)),
		Unchanged: plan.Unchanged,
		Skipped:   plan.Skipped,
	}
	for _, change := range plan.Changes {
		out.Changes = append(out.Changes, changeOutput{
			ID:      change.Setting.ID,
			Action:  change.Action(),
			Current: change.Current,
			Desired: change.Desired,
			Existed: change.Existed,
			Restart: change.Setting.Restart,
			Risk:    change.Setting.Risk,
		})
	}
	return out
}

func makeFailureOutput(failures []engine.Failure) []failureOutput {
	out := make([]failureOutput, 0, len(failures))
	for _, failure := range failures {
		out = append(out, failureOutput{
			ID:    failure.ID,
			Stage: failure.Stage,
			Error: failure.Err.Error(),
		})
	}
	return out
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) bool {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprintf(stderr, "usage: pear %s [flags]\n", fs.Name())
			fs.SetOutput(stderr)
			fs.PrintDefaults()
			return false
		}
		fmt.Fprintf(stderr, "pear %s: %v\n", fs.Name(), err)
		return false
	}
	return true
}

func resolveConfig(flagPath string) string {
	if strings.TrimSpace(flagPath) != "" {
		return config.ExpandPath(flagPath)
	}
	return config.DefaultPath()
}

func splitOnly(only string) []string {
	if strings.TrimSpace(only) == "" {
		return nil
	}
	parts := strings.Split(only, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	sort.Strings(out)
	return out
}

func requireDarwin(stderr io.Writer) bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	fmt.Fprintln(stderr, "pear: apply, plan, and restore require macOS (validate still works)")
	return false
}
