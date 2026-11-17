package dialog

import (
	"slices"
	"strconv"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"
)

// QueueID is the identifier for the prompt queue dialog.
const QueueID = "queue"

// QueueEntry is one prompt waiting in the agent's queue. Index is its
// position in the queue itself, which is what the removal call takes.
// Prompt is the raw queued text, so a dismissal still names its entry when
// the queue drains underneath the panel; Label is what the row shows.
type QueueEntry struct {
	Index  int
	Prompt string
	Label  string
}

// Display returns the row title: the prepared label when the caller
// supplied one, otherwise the prompt itself on a single line.
func (e QueueEntry) Display() string {
	if e.Label != "" {
		return e.Label
	}
	return singleLine(e.Prompt)
}

// Queue lists the prompts waiting for the running turn to finish, and lets
// the user drop individual prompts or clear the whole queue.
type Queue struct {
	com    *common.Common
	help   help.Model
	list   *list.FilterableList
	keyMap struct {
		Remove,
		Clear,
		UpDown,
		Next,
		Previous,
		Close key.Binding
	}
	scrolledToSelection bool
}

var _ Dialog = (*Queue)(nil)

// NewQueue creates a prompt queue dialog.
func NewQueue(com *common.Common, entries []QueueEntry) *Queue {
	d := &Queue{com: com}

	helpModel := help.New()
	helpModel.Styles = com.Styles.DialogHelpStyles()
	d.help = helpModel

	d.list = list.NewFilterableList(queueItems(com.Styles, entries)...)
	d.list.Focus()
	d.list.SetSelected(0)

	d.keyMap.Remove = key.NewBinding(
		key.WithKeys("x", "delete", "backspace"),
		key.WithHelp("x", "remove"),
	)
	d.keyMap.Clear = key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl+x", "clear all"),
	)
	d.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	d.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next prompt"),
	)
	d.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous prompt"),
	)
	d.keyMap.Close = CloseKey

	return d
}

// ID implements Dialog.
func (d *Queue) ID() string {
	return QueueID
}

// SetEntries replaces the listed prompts, keeping the selection in range.
// The panel is kept open across queue changes so it can follow the agent
// while it works through the queue.
func (d *Queue) SetEntries(entries []QueueEntry) {
	if slices.Equal(d.entries(), entries) {
		return
	}
	d.list.SetItems(queueItems(d.com.Styles, entries)...)
	d.list.SetFilter("")
	if d.list.Len() > 0 {
		d.list.SetSelected(min(d.list.Selected(), d.list.Len()-1))
		d.list.ScrollToSelected()
	}
}

// entries returns the prompts currently listed, in queue order.
func (d *Queue) entries() []QueueEntry {
	items := d.list.FilteredItems()
	entries := make([]QueueEntry, 0, len(items))
	for _, item := range items {
		if queueItem, ok := item.(*QueueItem); ok && queueItem != nil {
			entries = append(entries, queueItem.Entry)
		}
	}
	return entries
}

// HandleMsg implements Dialog.
func (d *Queue) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, d.keyMap.Clear):
			if len(d.entries()) == 0 {
				return nil
			}
			d.list.SetItems()
			return ActionClearQueue{}
		case key.Matches(msg, d.keyMap.Remove):
			item, ok := d.list.SelectedItem().(*QueueItem)
			if !ok || item == nil {
				return nil
			}
			entries := d.entries()
			remaining := slices.DeleteFunc(entries, func(e QueueEntry) bool {
				return e.Index == item.Entry.Index
			})
			selected := d.list.Selected()
			d.list.SetItems(queueItems(d.com.Styles, remaining)...)
			d.list.SetFilter("")
			if d.list.Len() > 0 {
				d.list.SetSelected(min(selected, d.list.Len()-1))
				d.list.ScrollToSelected()
			}
			return ActionRemoveQueuedPrompt{Index: item.Entry.Index, Prompt: item.Entry.Prompt}
		case key.Matches(msg, d.keyMap.Previous):
			d.list.Focus()
			if d.list.IsSelectedFirst() {
				d.list.SelectLast()
			} else {
				d.list.SelectPrev()
			}
			d.list.ScrollToSelected()
		case key.Matches(msg, d.keyMap.Next):
			d.list.Focus()
			if d.list.IsSelectedLast() {
				d.list.SelectFirst()
			} else {
				d.list.SelectNext()
			}
			d.list.ScrollToSelected()
		}
	case common.CoalescedWheelMsg:
		d.list.ScrollBy(int(msg.DeltaY))
	}
	return nil
}

// Draw implements Dialog.
func (d *Queue) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	listHeight, listTotalHeight, _ := sizeDialogListBare(t, d.list, innerWidth, height)

	if !d.scrolledToSelection {
		d.scrolledToSelection = true
		d.list.ScrollToSelected()
	}

	rc := NewRenderContext(t, width)
	rc.Title = "Prompt queue"
	if d.list.Len() == 0 {
		rc.AddPart(t.Dialog.List.Render("Nothing queued. Type while the agent works to queue a prompt."))
	} else {
		bodyView := t.Dialog.List.Height(d.list.Height()).Render(d.list.Render())
		bodyView = joinScrollbar(t, bodyView, listHeight, listTotalHeight, listHeight, d.list.Offset())
		rc.AddPart(bodyView)
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)

	DrawCenter(scr, area, rc.Render())
	return nil
}

// ShortHelp implements help.KeyMap.
func (d *Queue) ShortHelp() []key.Binding {
	return []key.Binding{
		d.keyMap.UpDown,
		d.keyMap.Remove,
		d.keyMap.Clear,
		d.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (d *Queue) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{d.keyMap.Remove, d.keyMap.Clear, d.keyMap.Next, d.keyMap.Previous},
		{d.keyMap.Close},
	}
}

// QueueItem wraps a QueueEntry to implement ListItem.
type QueueItem struct {
	*list.Versioned
	Entry   QueueEntry
	t       *styles.Styles
	m       fuzzy.Match
	cache   map[int]string
	focused bool
}

var _ ListItem = (*QueueItem)(nil)

// Finished implements list.Item.
func (i *QueueItem) Finished() bool { return true }

// Filter returns the value searched when filtering the list.
func (i *QueueItem) Filter() string { return i.Entry.Display() }

// ID returns a stable identifier for the entry's queue position.
func (i *QueueItem) ID() string {
	return strconv.Itoa(i.Entry.Index)
}

// SetMatch implements list.MatchSettable.
func (i *QueueItem) SetMatch(m fuzzy.Match) {
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
func (i *QueueItem) SetFocused(focused bool) {
	if i.focused == focused {
		return
	}
	i.cache = nil
	i.focused = focused
	if i.Versioned != nil {
		i.Bump()
	}
}

// Render draws the prompt on one line with its queue position on the right.
func (i *QueueItem) Render(width int) string {
	styles := ListItemStyles{
		ItemBlurred:     i.t.Dialog.NormalItem,
		ItemFocused:     i.t.Dialog.SelectedItem,
		InfoTextBlurred: i.t.Dialog.Sessions.InfoBlurred,
		InfoTextFocused: i.t.Dialog.Sessions.InfoFocused,
	}
	return renderItem(styles, i.Entry.Display(), "#"+strconv.Itoa(i.Entry.Index+1), i.focused, width, i.cache, &i.m)
}

// queueItems converts entries into list items.
func queueItems(t *styles.Styles, entries []QueueEntry) []list.FilterableItem {
	items := make([]list.FilterableItem, len(entries))
	for i, entry := range entries {
		items[i] = &QueueItem{Versioned: list.NewVersioned(), Entry: entry, t: t}
	}
	return items
}
