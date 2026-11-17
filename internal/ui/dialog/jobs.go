package dialog

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"
)

// JobsID is the identifier for the background jobs dialog.
const JobsID = "jobs"

// JobEntry is one background job as the panel lists it.
type JobEntry struct {
	ID          string
	Command     string
	Description string
	WorkingDir  string
	Running     bool
	ExitCode    int
	StartedAt   int64
	CompletedAt int64
}

// Label is the one-line title shown for the job: its description when the
// agent gave one, otherwise the command itself.
func (e JobEntry) Label() string {
	if label := strings.TrimSpace(e.Description); label != "" {
		return label
	}
	return strings.Join(strings.Fields(e.Command), " ")
}

// Duration reports how long the job has been (or was) running.
func (e JobEntry) Duration(now time.Time) time.Duration {
	start := time.Unix(e.StartedAt, 0)
	end := now
	if !e.Running && e.CompletedAt > 0 {
		end = time.Unix(e.CompletedAt, 0)
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Truncate(time.Second)
}

// JobOutput is the retained tail of a job's output.
type JobOutput struct {
	JobID    string
	Command  string
	Running  bool
	ExitCode int
	Output   string
}

type jobsMode int

const (
	jobsModeList jobsMode = iota
	jobsModeOutput
)

// Jobs lists the background shells the agent started so the user can watch
// one or terminate it. Selecting a job switches the panel to its output;
// escape returns to the list.
type Jobs struct {
	com    *common.Common
	help   help.Model
	list   *list.FilterableList
	mode   jobsMode
	keyMap struct {
		Select,
		Kill,
		Refresh,
		Back,
		Close,
		UpDown,
		PageUp,
		PageDown key.Binding
	}
	scrolledToSelection bool

	// Output mode state.
	output    JobOutput
	outputAt  int
	outputEnd bool
	// height is the output body height reported by the last draw, so the
	// scroll keys can page and clamp without waiting for a redraw.
	height int
}

var _ Dialog = (*Jobs)(nil)

// NewJobs creates an empty background jobs dialog. Jobs arrive through
// SetJobs once the workspace answers.
func NewJobs(com *common.Common) *Jobs {
	d := &Jobs{com: com}

	helpModel := help.New()
	helpModel.Styles = com.Styles.DialogHelpStyles()
	d.help = helpModel

	d.list = list.NewFilterableList()
	d.list.Focus()

	d.keyMap.Select = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "view output"),
	)
	d.keyMap.Kill = key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "kill job"),
	)
	d.keyMap.Refresh = key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	)
	d.keyMap.Back = key.NewBinding(
		key.WithKeys("esc", "alt+esc"),
		key.WithHelp("esc", "back"),
	)
	d.keyMap.Close = CloseKey
	d.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	d.keyMap.PageUp = key.NewBinding(
		key.WithKeys("pgup", "b"),
		key.WithHelp("b/pgup", "page up"),
	)
	d.keyMap.PageDown = key.NewBinding(
		key.WithKeys("pgdown", " ", "f"),
		key.WithHelp("f/pgdn", "page down"),
	)

	return d
}

// ID implements Dialog.
func (d *Jobs) ID() string {
	return JobsID
}

// SetJobs replaces the listed jobs, keeping the selection on the same job
// when it survives the refresh. Unchanged snapshots are ignored so the
// per-second poll does not rebuild every row.
func (d *Jobs) SetJobs(entries []JobEntry) {
	if slices.Equal(d.entries(), entries) {
		return
	}
	selectedID := d.SelectedJobID()
	d.list.SetItems(jobItems(d.com.Styles, entries)...)
	d.list.SetFilter("")
	if d.list.Len() == 0 {
		return
	}

	index := 0
	for i, entry := range entries {
		if entry.ID == selectedID {
			index = i
			break
		}
	}
	d.list.SetSelected(min(index, d.list.Len()-1))
	d.list.ScrollToSelected()
	d.scrolledToSelection = true
}

// entries returns the jobs currently listed.
func (d *Jobs) entries() []JobEntry {
	items := d.list.FilteredItems()
	entries := make([]JobEntry, 0, len(items))
	for _, item := range items {
		if jobItem, ok := item.(*JobItem); ok && jobItem != nil {
			entries = append(entries, jobItem.Entry)
		}
	}
	return entries
}

// SelectedJobID returns the job the selection sits on, or "" when the list
// is empty.
func (d *Jobs) SelectedJobID() string {
	if item, ok := d.list.SelectedItem().(*JobItem); ok && item != nil {
		return item.Entry.ID
	}
	return ""
}

// ShowOutput switches the panel to the output view for a job.
func (d *Jobs) ShowOutput(out JobOutput) {
	// A refresh of the job already on screen must not yank the view back
	// to the tail while the user is reading further up.
	sameJob := d.mode == jobsModeOutput && d.output.JobID == out.JobID
	d.mode = jobsModeOutput
	d.output = out
	if sameJob {
		return
	}
	// Newest output is what the user came to see, so follow the tail and
	// let the next draw pin the window to the end.
	d.outputAt = 0
	d.outputEnd = true
}

// ShowList returns the panel to the job list.
func (d *Jobs) ShowList() {
	d.mode = jobsModeList
	d.output = JobOutput{}
	d.outputAt = 0
	d.outputEnd = false
}

// Mode reports which view is showing.
func (d *Jobs) InOutput() bool {
	return d.mode == jobsModeOutput
}

// OutputJobID returns the job whose output is showing, or "" in list mode.
func (d *Jobs) OutputJobID() string {
	if d.mode != jobsModeOutput {
		return ""
	}
	return d.output.JobID
}

// HandleMsg implements Dialog.
func (d *Jobs) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if d.mode == jobsModeOutput {
			return d.handleOutputKey(msg)
		}
		return d.handleListKey(msg)
	case common.CoalescedWheelMsg:
		if d.mode == jobsModeOutput {
			d.scrollOutput(int(msg.DeltaY))
		} else {
			d.list.ScrollBy(int(msg.DeltaY))
		}
	}
	return nil
}

func (d *Jobs) handleListKey(msg tea.KeyPressMsg) Action {
	switch {
	case key.Matches(msg, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(msg, d.keyMap.Kill):
		if id := d.SelectedJobID(); id != "" {
			return ActionKillJob{ID: id}
		}
	case key.Matches(msg, d.keyMap.Select):
		if id := d.SelectedJobID(); id != "" {
			return ActionJobOutputRequest{ID: id}
		}
	case key.Matches(msg, d.keyMap.Refresh):
		return ActionJobsRefresh{}
	case key.Matches(msg, d.keyMap.PageUp):
		d.list.SelectFirst()
		d.list.ScrollToSelected()
	case key.Matches(msg, d.keyMap.PageDown):
		d.list.SelectLast()
		d.list.ScrollToSelected()
	case msg.Code == tea.KeyUp:
		d.list.Focus()
		if d.list.IsSelectedFirst() {
			d.list.SelectLast()
		} else {
			d.list.SelectPrev()
		}
		d.list.ScrollToSelected()
	case msg.Code == tea.KeyDown:
		d.list.Focus()
		if d.list.IsSelectedLast() {
			d.list.SelectFirst()
		} else {
			d.list.SelectNext()
		}
		d.list.ScrollToSelected()
	}
	return nil
}

func (d *Jobs) handleOutputKey(msg tea.KeyPressMsg) Action {
	switch {
	case key.Matches(msg, d.keyMap.Back):
		// Escape unwinds one level: output first, then the list.
		d.ShowList()
		return nil
	case key.Matches(msg, d.keyMap.Kill):
		if d.output.JobID != "" {
			return ActionKillJob{ID: d.output.JobID}
		}
	case key.Matches(msg, d.keyMap.Refresh):
		return ActionJobOutputRequest{ID: d.output.JobID}
	case msg.Code == tea.KeyUp:
		d.scrollOutput(-1)
	case msg.Code == tea.KeyDown:
		d.scrollOutput(1)
	case key.Matches(msg, d.keyMap.PageUp):
		d.scrollOutput(-d.outputHeight())
	case key.Matches(msg, d.keyMap.PageDown):
		d.scrollOutput(d.outputHeight())
	}
	return nil
}

// outputHeight is how many output lines fit, kept in sync by Draw.
func (d *Jobs) outputHeight() int {
	return max(1, d.height)
}

// syncOutputWindow records the body height from the last draw and keeps the
// scroll offset inside the retained lines. A view parked at the end follows
// the job as it keeps writing.
func (d *Jobs) syncOutputWindow(h int) {
	d.height = max(1, h)
	maxOffset := max(0, len(jobOutputLines(d.output))-d.height)
	if d.outputEnd {
		d.outputAt = maxOffset
	}
	d.outputAt = max(0, min(d.outputAt, maxOffset))
	d.outputEnd = d.outputAt >= maxOffset
}

func (d *Jobs) scrollOutput(delta int) {
	maxOffset := max(0, len(jobOutputLines(d.output))-d.outputHeight())
	d.outputAt = max(0, min(d.outputAt+delta, maxOffset))
	d.outputEnd = d.outputAt >= maxOffset
}

// Draw implements Dialog.
func (d *Jobs) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()

	rc := NewRenderContext(t, width)
	if d.mode == jobsModeOutput {
		d.drawOutput(t, rc, innerWidth, height)
	} else {
		d.drawList(t, rc, height, innerWidth)
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)
	DrawCenter(scr, area, rc.Render())
	return nil
}

func (d *Jobs) drawList(t *styles.Styles, rc *RenderContext, height, innerWidth int) {
	rc.Title = "Background jobs"
	if d.list.Len() == 0 {
		rc.AddPart(t.Dialog.List.Render("No background jobs. Commands the agent leaves running appear here."))
		return
	}
	listHeight, listTotalHeight, _ := sizeDialogListBare(t, d.list, innerWidth, height)
	if !d.scrolledToSelection {
		d.scrolledToSelection = true
		d.list.ScrollToSelected()
	}
	bodyView := t.Dialog.List.Height(d.list.Height()).Render(d.list.Render())
	bodyView = joinScrollbar(t, bodyView, listHeight, listTotalHeight, listHeight, d.list.Offset())
	rc.AddPart(bodyView)
}

func (d *Jobs) drawOutput(t *styles.Styles, rc *RenderContext, innerWidth, height int) {
	title := "Job " + d.output.JobID
	rc.Title = title

	status := "running"
	if !d.output.Running {
		status = "finished"
		if d.output.ExitCode != 0 {
			status = fmt.Sprintf("failed (exit %d)", d.output.ExitCode)
		}
	}
	header := t.Dialog.List.Render(fmt.Sprintf("%s · %s", status, singleLine(d.output.Command)))
	rc.AddPart(header)

	// The header and the dialog chrome come out of the output budget.
	bodyHeight := max(1, height-3)
	d.syncOutputWindow(bodyHeight)

	lines := jobOutputLines(d.output)
	if len(lines) == 0 {
		rc.AddPart(t.Dialog.List.Render("No output yet."))
		return
	}
	window := lines[d.outputAt:min(d.outputAt+bodyHeight, len(lines))]
	body := strings.Join(window, "\n")
	bodyView := t.Dialog.List.Width(innerWidth).Height(bodyHeight).Render(body)
	rc.AddPart(bodyView)
}

// ShortHelp implements help.KeyMap.
func (d *Jobs) ShortHelp() []key.Binding {
	if d.mode == jobsModeOutput {
		return []key.Binding{d.keyMap.UpDown, d.keyMap.Kill, d.keyMap.Back}
	}
	return []key.Binding{d.keyMap.UpDown, d.keyMap.Select, d.keyMap.Kill, d.keyMap.Close}
}

// FullHelp implements help.KeyMap.
func (d *Jobs) FullHelp() [][]key.Binding {
	if d.mode == jobsModeOutput {
		return [][]key.Binding{
			{d.keyMap.UpDown, d.keyMap.PageUp, d.keyMap.PageDown},
			{d.keyMap.Refresh, d.keyMap.Kill, d.keyMap.Back},
		}
	}
	return [][]key.Binding{
		{d.keyMap.UpDown, d.keyMap.Select, d.keyMap.Refresh},
		{d.keyMap.Kill, d.keyMap.Close},
	}
}

// jobOutputLines splits retained output into display lines, dropping the
// trailing empty line that a final newline leaves behind.
func jobOutputLines(out JobOutput) []string {
	text := strings.TrimRight(out.Output, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// singleLine collapses whitespace so a command or status renders on one
// row.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// JobItem wraps a JobEntry to implement ListItem.
type JobItem struct {
	*list.Versioned
	Entry   JobEntry
	t       *styles.Styles
	m       fuzzy.Match
	cache   map[int]string
	focused bool
}

var _ ListItem = (*JobItem)(nil)

// Finished implements list.Item. Running jobs keep animating their
// duration, so they must not freeze in the list cache; finished ones can.
func (i *JobItem) Finished() bool { return !i.Entry.Running }

// Filter returns the value searched when filtering the list.
func (i *JobItem) Filter() string { return i.Entry.Label() }

// ID returns the identifier of the underlying job.
func (i *JobItem) ID() string { return i.Entry.ID }

// SetMatch implements list.MatchSettable.
func (i *JobItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(i.m, m) {
		return
	}
	i.cache = nil
	i.m = m
	if i.Versioned != nil {
		i.Bump()
	}
}

// SetFocused implements list.Focusable.
func (i *JobItem) SetFocused(focused bool) {
	if i.focused == focused {
		return
	}
	i.cache = nil
	i.focused = focused
	if i.Versioned != nil {
		i.Bump()
	}
}

// Render draws the job on one line with its status and duration or exit
// code on the right.
func (i *JobItem) Render(width int) string {
	styles := ListItemStyles{
		ItemBlurred:     i.t.Dialog.NormalItem,
		ItemFocused:     i.t.Dialog.SelectedItem,
		InfoTextBlurred: i.t.Dialog.Sessions.InfoBlurred,
		InfoTextFocused: i.t.Dialog.Sessions.InfoFocused,
	}

	icon := "✓"
	switch {
	case i.Entry.Running:
		icon = "●"
	case i.Entry.ExitCode != 0:
		icon = "✗"
	}
	title := icon + " " + singleLine(i.Entry.Label())

	info := "#" + i.Entry.ID
	// A running job's row changes every second, so it must not go through
	// the per-width render memo.
	var cache map[int]string
	if i.Entry.Running {
		info += " " + i.Entry.Duration(time.Now()).String()
	} else {
		cache = i.cache
		if i.Entry.ExitCode != 0 {
			info += fmt.Sprintf(" exit %d", i.Entry.ExitCode)
		}
	}

	return renderItem(styles, title, info, i.focused, width, cache, &i.m)
}

// jobItems converts entries into list items.
func jobItems(t *styles.Styles, entries []JobEntry) []list.FilterableItem {
	items := make([]list.FilterableItem, len(entries))
	for i, entry := range entries {
		items[i] = &JobItem{Versioned: list.NewVersioned(), Entry: entry, t: t}
	}
	return items
}
