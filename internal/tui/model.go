package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/metru/pear/internal/catalog"
	"github.com/metru/pear/internal/config"
	"github.com/metru/pear/internal/engine"
	"github.com/metru/pear/internal/value"
)

type screenKind int

const (
	screenList screenKind = iota
	screenEdit
	screenConfirm
	screenBusy
	screenResult
	screenError
)

type busyKind int

const (
	busySaving busyKind = iota
	busyApplying
)

type rootState struct {
	values map[string]any
	resets map[string]bool
}

// Model is the configure TUI.
type Model struct {
	opts    Options
	cat     *catalog.Catalog
	path    string
	cfg     *config.Config
	base    config.State
	state   config.State
	initial rootState
	current map[string]currentValue
	rows    []*row
	list    list.Model
	filter  filterMode

	screen  screenKind
	edit    *editModel
	confirm *confirmModel
	busy    busyKind
	spinner spinner.Model

	width  int
	height int
	dark   bool
	styles styles

	resultText string
	errorText  string
	statusMsg  string
}

// New builds the editor model.
func New(opts Options) (*Model, error) {
	if opts.Catalog == nil {
		return nil, fmt.Errorf("configure: catalog is required")
	}
	for _, selector := range opts.Only {
		if !opts.Catalog.HasPrefix(selector) {
			return nil, fmt.Errorf("configure: unknown selector %q", selector)
		}
	}

	path, err := config.ResolvePath(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	cfg, err := loadOrCreate(path, opts)
	if err != nil {
		return nil, err
	}

	settings := filteredSettings(opts.Catalog, opts.Only)
	if len(settings) == 0 {
		return nil, fmt.Errorf("configure: no settings match %q", strings.Join(opts.Only, ","))
	}

	m := &Model{
		opts:    opts,
		cat:     opts.Catalog,
		path:    path,
		cfg:     cfg,
		base:    cloneState(cfg.Inherited),
		state:   stateFromConfig(cfg),
		initial: rootFromConfig(cfg),
		current: make(map[string]currentValue, len(settings)),
		filter:  filterAll,
		width:   80,
		height:  24,
		dark:    true,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.styles = newStyles(m.dark)
	m.rebuildRows()
	m.list = m.newList()
	return m, nil
}

func loadOrCreate(path string, opts Options) (*config.Config, error) {
	if _, err := os.Stat(path); err == nil {
		return config.Load(path, opts.Catalog)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if !opts.AllowCreate {
		return nil, fmt.Errorf("config not found: %s", path)
	}
	return emptyConfig(path), nil
}

func filteredSettings(cat *catalog.Catalog, only []string) []catalog.Setting {
	if len(only) == 0 {
		return cat.All()
	}
	var out []catalog.Setting
	for _, setting := range cat.All() {
		for _, selector := range only {
			if setting.ID == selector || strings.HasPrefix(setting.ID, selector+".") {
				out = append(out, setting)
				break
			}
		}
	}
	return out
}

func rootFromConfig(cfg *config.Config) rootState {
	r := rootState{values: map[string]any{}, resets: map[string]bool{}}
	for id, origin := range cfg.Origins {
		if samePath(origin, cfg.Path) {
			if v, ok := cfg.Values[id]; ok {
				r.values[id] = v
			}
		}
	}
	for id, origin := range cfg.ResetOrigins {
		if samePath(origin, cfg.Path) {
			r.resets[id] = true
		}
	}
	return r
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, tea.RequestBackgroundColor}
	if m.opts.CanApply && m.opts.Adapter != nil {
		cmds = append(cmds, loadCurrentCmd(m.cat, m.opts.Adapter))
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 40)
		m.height = max(msg.Height, 12)
		m.list.SetSize(m.width, max(m.height-8, 5))
		if m.edit != nil {
			m.edit.resize(m.width)
		}
		if m.confirm != nil {
			m.confirm.resize(m.width)
		}
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.styles = newStyles(m.dark)
		m.rebuildList()
		return m, nil
	case currentLoadedMsg:
		m.current = msg.values
		m.rebuildRows()
		m.refreshList()
		m.statusMsg = ""
		return m, nil
	case savedMsg:
		return m.handleSaved(msg)
	case applyDoneMsg:
		return m.handleApplyDone(msg)
	case spinner.TickMsg:
		if m.screen != screenBusy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	switch m.screen {
	case screenEdit:
		return m.updateEdit(msg)
	case screenConfirm:
		return m.updateConfirm(msg)
	case screenResult, screenError:
		return m.updateResult(msg)
	case screenBusy:
		return m, nil
	default:
		return m.updateList(msg)
	}
}

func (m *Model) updateEdit(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.edit == nil {
		m.screen = screenList
		return m, nil
	}
	form, cmd := m.edit.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.edit.form = f
	}
	switch m.edit.form.State {
	case huh.StateCompleted:
		v, err := m.edit.Value()
		if err != nil {
			m.screen = screenError
			m.errorText = err.Error()
			return m, nil
		}
		m.setValue(m.edit.setting.ID, v)
		m.screen = screenList
		m.edit = nil
		m.statusMsg = "Updated " + m.statusMsgID() + " (unsaved)"
		m.rebuildRows()
		m.refreshList()
		return m, nil
	case huh.StateAborted:
		m.screen = screenList
		m.edit = nil
		return m, nil
	}
	return m, cmd
}

func (m *Model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.confirm == nil {
		m.screen = screenList
		return m, nil
	}
	form, cmd := m.confirm.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.confirm.form = f
	}
	switch m.confirm.form.State {
	case huh.StateCompleted:
		confirm := m.confirm
		m.confirm = nil
		m.screen = screenList
		if !confirm.value {
			return m, nil
		}
		switch confirm.action {
		case confirmApply:
			m.screen = screenBusy
			m.busy = busyApplying
			m.statusMsg = ""
			return m, tea.Batch(m.spinner.Tick, applyCmd(m.cat, m.path, m.opts.Only))
		case confirmQuit:
			return m, tea.Quit
		case confirmReset:
			m.resetSetting(confirm.id)
			m.rebuildRows()
			m.refreshList()
			m.statusMsg = "Marked " + confirm.id + " reset (unsaved)"
		}
		return m, nil
	case huh.StateAborted:
		m.confirm = nil
		m.screen = screenList
		return m, nil
	}
	return m, cmd
}

func (m *Model) updateResult(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "enter", "esc", "q":
		m.screen = screenList
		m.errorText = ""
		m.resultText = ""
		return m, nil
	}
	return m, nil
}

func (m *Model) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	if m.list.FilterState() == list.Filtering {
		switch key.String() {
		case "ctrl+c":
			return m.requestQuit()
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	switch key.String() {
	case "ctrl+c", "q":
		return m.requestQuit()
	case "tab":
		m.filter = m.filter.next()
		m.refreshList()
		return m, nil
	case "enter":
		if r := m.selectedRow(); r != nil {
			m.edit = newEditModel(r, m.width, m.dark)
			m.screen = screenEdit
			return m, m.edit.form.Init()
		}
		return m, nil
	case "u":
		if r := m.selectedRow(); r != nil {
			if message := m.removeRootDirective(r.setting.ID); message != "" {
				m.statusMsg = message
				return m, nil
			}
			m.rebuildRows()
			m.refreshList()
			m.statusMsg = "Removed " + r.setting.ID + " from config (unsaved)"
		}
		return m, nil
	case "x":
		if r := m.selectedRow(); r != nil {
			m.confirm = newConfirm(
				confirmReset,
				r.setting.ID,
				"Reset "+r.setting.ID+"?",
				"This adds a reset to the config. Applying will delete the macOS preference.",
				"Reset", "Cancel", false, m.width, m.dark,
			)
			m.screen = screenConfirm
			return m, m.confirm.form.Init()
		}
		return m, nil
	case "ctrl+s":
		edits := m.pendingEdits()
		if len(edits) == 0 {
			m.statusMsg = "No changes to save"
			return m, nil
		}
		m.screen = screenBusy
		m.busy = busySaving
		m.statusMsg = ""
		return m, tea.Batch(m.spinner.Tick, saveCmd(m.path, m.cat, edits))
	case "ctrl+r":
		if m.opts.CanApply && m.opts.Adapter != nil {
			m.statusMsg = "Refreshing macOS values..."
			return m, loadCurrentCmd(m.cat, m.opts.Adapter)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *Model) requestQuit() (tea.Model, tea.Cmd) {
	if len(m.pendingEdits()) == 0 {
		return m, tea.Quit
	}
	m.confirm = newConfirm(
		confirmQuit, "",
		"Discard unsaved changes?",
		fmt.Sprintf("You have %s that have not been written.", editCount(len(m.pendingEdits()))),
		"Discard", "Keep editing", false, m.width, m.dark,
	)
	m.screen = screenConfirm
	return m, m.confirm.form.Init()
}

func (m *Model) handleSaved(msg savedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenError
		m.errorText = msg.err.Error()
		return m, nil
	}
	oldPath := m.path
	m.cfg = msg.cfg
	m.path = msg.cfg.Path
	m.base = cloneState(msg.cfg.Inherited)
	m.state = stateFromConfig(msg.cfg)
	m.initial = rootFromConfig(msg.cfg)
	m.rebuildRows()
	if !samePath(oldPath, m.path) {
		m.rebuildList()
	} else {
		m.refreshList()
	}
	m.statusMsg = fmt.Sprintf("Saved %s to %s", editCount(msg.edits), compactHome(m.path))
	if msg.backup != "" {
		m.statusMsg += " (backup: " + compactHome(msg.backup) + ")"
	}
	if m.opts.CanApply && m.pendingCount() > 0 {
		m.confirm = newConfirm(
			confirmApply, "",
			"Apply changes now?",
			fmt.Sprintf("%d setting(s) will be written to macOS.", m.pendingCount()),
			"Apply", "Later", true, m.width, m.dark,
		)
		m.screen = screenConfirm
		return m, m.confirm.form.Init()
	}
	m.screen = screenList
	return m, nil
}

func (m *Model) handleApplyDone(msg applyDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenError
		m.errorText = msg.err.Error()
		return m, nil
	}
	m.screen = screenResult
	m.resultText = formatResult(msg.result)
	if m.opts.CanApply && m.opts.Adapter != nil {
		return m, loadCurrentCmd(m.cat, m.opts.Adapter)
	}
	return m, nil
}

func (m *Model) setValue(id string, v any) {
	m.state.Values[id] = v
	m.state.Origins[id] = m.path
	removeReset(&m.state, id)
}

func (m *Model) resetSetting(id string) {
	delete(m.state.Values, id)
	delete(m.state.Origins, id)
	addReset(&m.state, id, m.path)
}

// removeRootDirective removes a root value or reset. It reveals an inherited
// value/reset underneath, and refuses to edit an include.
func (m *Model) removeRootDirective(id string) string {
	rootValue := samePath(m.state.Origins[id], m.path)
	rootReset := samePath(m.state.ResetOrigins[id], m.path) && m.state.HasReset(id)

	if !rootValue && !rootReset {
		if origin := m.state.Origins[id]; origin != "" {
			return fmt.Sprintf("%s is defined in %s; press x to add a reset", id, compactHome(origin))
		}
		if origin := m.state.ResetOrigins[id]; origin != "" {
			return fmt.Sprintf("%s is reset by %s", id, compactHome(origin))
		}
		return id + " is not configured"
	}

	delete(m.state.Values, id)
	delete(m.state.Origins, id)
	removeReset(&m.state, id)

	if v, ok := m.base.Values[id]; ok {
		m.state.Values[id] = v
		m.state.Origins[id] = m.base.Origins[id]
		return ""
	}
	if m.base.HasReset(id) {
		addReset(&m.state, id, m.base.ResetOrigins[id])
	}
	return ""
}

func (m *Model) pendingEdits() []config.Edit {
	desiredValues := map[string]any{}
	for id, origin := range m.state.Origins {
		if !samePath(origin, m.path) {
			continue
		}
		if v, ok := m.state.Values[id]; ok {
			desiredValues[id] = v
		}
	}
	desiredResets := map[string]bool{}
	for id, origin := range m.state.ResetOrigins {
		if samePath(origin, m.path) && m.state.HasReset(id) {
			desiredResets[id] = true
		}
	}

	ids := map[string]bool{}
	for id := range m.initial.values {
		ids[id] = true
	}
	for id := range m.initial.resets {
		ids[id] = true
	}
	for id := range desiredValues {
		ids[id] = true
	}
	for id := range desiredResets {
		ids[id] = true
	}

	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	var edits []config.Edit
	for _, id := range sorted {
		iv, hasValue := m.initial.values[id]
		ir := m.initial.resets[id]
		dv, wantValue := desiredValues[id]
		dr := desiredResets[id]

		switch {
		case hasValue && wantValue:
			if !value.Equal(iv, dv) {
				edits = append(edits, config.Edit{Kind: config.SetValue, ID: id, Value: dv})
			}
		case hasValue && !wantValue && dr:
			edits = append(edits, config.Edit{Kind: config.ResetSetting, ID: id})
		case hasValue && !wantValue && !dr:
			edits = append(edits, config.Edit{Kind: config.RemoveValue, ID: id})
		case !hasValue && wantValue:
			edits = append(edits, config.Edit{Kind: config.SetValue, ID: id, Value: dv})
		case !hasValue && !wantValue && dr && !ir:
			edits = append(edits, config.Edit{Kind: config.ResetSetting, ID: id})
		case ir && !dr:
			edits = append(edits, config.Edit{Kind: config.UnresetSetting, ID: id})
		}
	}
	return edits
}

func (m *Model) rebuildRows() {
	m.rows = make([]*row, 0, len(m.settings()))
	for _, setting := range m.settings() {
		r := &row{setting: setting, status: statusUnset}
		if v, ok := m.state.Values[setting.ID]; ok {
			r.status = statusSet
			r.value = v
			r.origin = m.state.Origins[setting.ID]
		} else if m.state.HasReset(setting.ID) {
			r.status = statusReset
			r.origin = m.state.ResetOrigins[setting.ID]
		}
		r.current = m.current[setting.ID]
		r.pending = m.isPending(r)
		m.rows = append(m.rows, r)
	}
}

func (m *Model) settings() []catalog.Setting {
	return filteredSettings(m.cat, m.opts.Only)
}

func (m *Model) isPending(r *row) bool {
	if !m.opts.CanApply || !r.current.known {
		return false
	}
	switch r.status {
	case statusSet:
		if !r.current.found {
			return true
		}
		return !value.Equal(r.current.value, r.value)
	case statusReset:
		return r.current.found
	default:
		return false
	}
}

func (m *Model) pendingCount() int {
	count := 0
	for _, r := range m.rows {
		if r.pending {
			count++
		}
	}
	return count
}

func (m *Model) visibleRows() []*row {
	out := make([]*row, 0, len(m.rows))
	for _, r := range m.rows {
		switch m.filter {
		case filterConfigured:
			if r.status == statusUnset {
				continue
			}
		case filterUnset:
			if r.status != statusUnset {
				continue
			}
		case filterPending:
			if !r.pending {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func (m *Model) newList() list.Model {
	d := delegate{styles: m.styles, configPath: m.path, showCurrent: m.opts.CanApply}
	l := list.New(m.listItems(), d, m.width, max(m.height-8, 5))
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.SetShowFilter(true)
	l.DisableQuitKeybindings()
	l.FilterInput.Placeholder = "search settings"
	km := list.DefaultKeyMap()
	km.CursorUp.SetKeys("up")
	km.CursorDown.SetKeys("down")
	km.NextPage.SetKeys("right", "pgdown")
	km.PrevPage.SetKeys("left", "pgup")
	km.GoToStart.SetKeys("home")
	km.GoToEnd.SetKeys("end")
	km.Filter.SetKeys("/")
	km.ClearFilter.SetKeys("esc")
	l.KeyMap = km
	return l
}

func (m *Model) listItems() []list.Item {
	rows := m.visibleRows()
	items := make([]list.Item, len(rows))
	for i, r := range rows {
		items[i] = r
	}
	return items
}

func (m *Model) refreshList() {
	selectedID := ""
	if r := m.selectedRow(); r != nil {
		selectedID = r.setting.ID
	}
	filter := m.list.FilterValue()
	items := m.listItems()
	m.list.SetItems(items)
	if filter != "" {
		m.list.SetFilterText(filter)
	}
	for i, item := range items {
		if r, ok := item.(*row); ok && r.setting.ID == selectedID {
			m.list.Select(i)
			break
		}
	}
}

func (m *Model) rebuildList() {
	selectedID := ""
	if r := m.selectedRow(); r != nil {
		selectedID = r.setting.ID
	}
	filter := m.list.FilterValue()
	m.list = m.newList()
	if filter != "" {
		m.list.SetFilterText(filter)
	}
	for i, item := range m.list.Items() {
		if r, ok := item.(*row); ok && r.setting.ID == selectedID {
			m.list.Select(i)
			break
		}
	}
}

func (m *Model) selectedRow() *row {
	item := m.list.SelectedItem()
	if item == nil {
		return nil
	}
	r, _ := item.(*row)
	return r
}

func (m *Model) statusMsgID() string {
	if r := m.selectedRow(); r != nil {
		return r.setting.ID
	}
	return "setting"
}

func addReset(s *config.State, id, origin string) {
	if !s.HasReset(id) {
		s.Resets = append(s.Resets, id)
		sort.Strings(s.Resets)
	}
	s.ResetOrigins[id] = origin
}

func removeReset(s *config.State, id string) {
	for i, existing := range s.Resets {
		if existing == id {
			s.Resets = append(s.Resets[:i], s.Resets[i+1:]...)
			break
		}
	}
	delete(s.ResetOrigins, id)
}

// Run starts the editor.
func Run(opts Options) error {
	model, err := New(opts)
	if err != nil {
		return err
	}
	program := tea.NewProgram(model)
	_, err = program.Run()
	return err
}

func formatResult(result *engine.ApplyResult) string {
	if result == nil {
		return "Nothing to apply."
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Applied %d change(s), %d unchanged.", result.Applied, result.Unchanged))
	if result.Skipped > 0 {
		lines = append(lines, fmt.Sprintf("%d skipped.", result.Skipped))
	}
	for _, target := range result.Restarts {
		lines = append(lines, "Restarted "+target+".")
	}
	if result.SnapshotDir != "" {
		lines = append(lines, "Snapshot: "+compactHome(result.SnapshotDir))
	}
	for _, failure := range result.Failures {
		lines = append(lines, "Failed: "+failure.String())
	}
	return strings.Join(lines, "\n")
}

func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "pear configure"
	return view
}

func (m *Model) render() string {
	if m.width <= 0 {
		m.width = 80
	}
	header := m.renderHeader()
	body := m.renderBody()
	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *Model) renderHeader() string {
	setCount, resetCount, unsetCount := 0, 0, 0
	for _, r := range m.rows {
		switch r.status {
		case statusSet:
			setCount++
		case statusReset:
			resetCount++
		default:
			unsetCount++
		}
	}
	title := m.styles.title.Render("pear configure")
	path := m.styles.subtitle.Render(compactHome(m.path))
	counts := m.styles.subtitle.Render(fmt.Sprintf(
		"%d set · %d reset · %d unset · %d pending · filter: %s",
		setCount, resetCount, unsetCount, m.pendingCount(), m.filter,
	))
	rule := m.styles.headerRule.Render(strings.Repeat("─", max(m.width, 20)))
	titleLine := ansi.Truncate(title+"  "+path, m.width, "…")
	return lipgloss.JoinVertical(lipgloss.Left, titleLine, ansi.Truncate(counts, m.width, "…"), rule)
}

func (m *Model) renderBody() string {
	switch m.screen {
	case screenEdit:
		if m.edit != nil {
			return m.edit.form.View()
		}
	case screenConfirm:
		if m.confirm != nil {
			return m.confirm.form.View()
		}
	case screenBusy:
		label := "Saving config…"
		if m.busy == busyApplying {
			label = "Applying settings…"
		}
		return "\n" + m.spinner.View() + " " + label + "\n"
	case screenResult:
		return "\n" + m.styles.box.Render(m.resultText) + "\n"
	case screenError:
		return "\n" + m.styles.box.Render(m.styles.danger.Render(m.errorText)) + "\n"
	}

	if len(m.visibleRows()) == 0 {
		message := "No settings match this filter."
		if m.list.FilterValue() != "" {
			message = "No settings match " + fmt.Sprintf("%q", m.list.FilterValue()) + "."
		}
		return "\n  " + m.styles.desc.Render(message) + "\n"
	}
	return m.list.View()
}

func (m *Model) renderFooter() string {
	var help string
	switch m.screen {
	case screenEdit:
		help = "enter submit · esc cancel"
	case screenConfirm:
		help = "←/→ choose · enter confirm · esc cancel"
	case screenResult, screenError:
		help = "enter close"
	case screenBusy:
		help = "please wait"
	default:
		help = "↑/↓ move · enter edit · u unset · x reset · tab filter · ctrl+s save · ctrl+r refresh · q quit"
	}
	lines := []string{}
	if m.statusMsg != "" {
		lines = append(lines, m.styles.footer.Render(ansi.Truncate(m.statusMsg, m.width, "…")))
	}
	lines = append(lines, m.styles.help.Render(ansi.Truncate(help, m.width, "…")))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
