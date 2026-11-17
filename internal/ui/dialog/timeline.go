package dialog

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/dustin/go-humanize"
	"github.com/sahilm/fuzzy"
)

// TimelineID is the identifier for the timeline rewind dialog.
const TimelineID = "timeline"

// TimelineEntry is one user prompt in a session transcript, a point the
// conversation can be rewound to.
type TimelineEntry struct {
	// MessageID is the user message that started the turn.
	MessageID string
	// Prompt is the text the user typed.
	Prompt string
	// CreatedAt is the unix timestamp of the prompt.
	CreatedAt int64
}

// Timeline lists the user's prompts so one can be rewound to. Choosing an
// entry drops that prompt and everything after it, and hands the prompt back
// to the editor.
type Timeline struct {
	com    *common.Common
	help   help.Model
	list   *list.FilterableList
	keyMap struct {
		Select,
		UpDown,
		Next,
		Previous,
		Close key.Binding
	}
	scrolledToSelection bool
}

var _ Dialog = (*Timeline)(nil)

// NewTimeline creates a timeline dialog. entries are expected in display
// order, most recent first.
func NewTimeline(com *common.Common, entries []TimelineEntry) *Timeline {
	d := &Timeline{com: com}

	helpModel := help.New()
	helpModel.Styles = com.Styles.DialogHelpStyles()
	d.help = helpModel

	d.list = list.NewFilterableList(timelineItems(com.Styles, entries)...)
	d.list.Focus()
	d.list.SetSelected(0)

	d.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "rewind here"),
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
func (d *Timeline) ID() string {
	return TimelineID
}

// HandleMsg implements Dialog.
func (d *Timeline) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Close):
			return ActionClose{}
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
		case key.Matches(msg, d.keyMap.Select):
			if item, ok := d.list.SelectedItem().(*TimelineItem); ok && item != nil {
				return ActionRewind{
					MessageID: item.Entry.MessageID,
					Prompt:    item.Entry.Prompt,
				}
			}
		}
	case common.CoalescedWheelMsg:
		d.list.ScrollBy(int(msg.DeltaY))
	}
	return nil
}

// Draw implements Dialog.
func (d *Timeline) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
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
	rc.Title = "Rewind conversation"
	if d.list.Len() == 0 {
		rc.AddPart(t.Dialog.List.Render("No prompts to rewind to yet."))
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
func (d *Timeline) ShortHelp() []key.Binding {
	return []key.Binding{
		d.keyMap.UpDown,
		d.keyMap.Select,
		d.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (d *Timeline) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{d.keyMap.Select, d.keyMap.Next, d.keyMap.Previous},
		{d.keyMap.Close},
	}
}

// TimelineItem wraps a TimelineEntry to implement ListItem.
type TimelineItem struct {
	*list.Versioned
	Entry   TimelineEntry
	t       *styles.Styles
	m       fuzzy.Match
	cache   map[int]string
	focused bool
}

var _ ListItem = (*TimelineItem)(nil)

// Finished implements list.Item. Timeline items are render-stable outside
// explicit focus and match changes.
func (i *TimelineItem) Finished() bool { return true }

// Filter returns the value searched when filtering the list.
func (i *TimelineItem) Filter() string { return i.Entry.Prompt }

// ID returns the identifier of the underlying message.
func (i *TimelineItem) ID() string { return i.Entry.MessageID }

// SetMatch implements list.MatchSettable.
func (i *TimelineItem) SetMatch(m fuzzy.Match) {
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
func (i *TimelineItem) SetFocused(focused bool) {
	if i.focused == focused {
		return
	}
	i.cache = nil
	i.focused = focused
	if i.Versioned != nil {
		i.Bump()
	}
}

// Render draws the prompt on one line with its timestamp on the right.
func (i *TimelineItem) Render(width int) string {
	styles := ListItemStyles{
		ItemBlurred:     i.t.Dialog.NormalItem,
		ItemFocused:     i.t.Dialog.SelectedItem,
		InfoTextBlurred: i.t.Dialog.Sessions.InfoBlurred,
		InfoTextFocused: i.t.Dialog.Sessions.InfoFocused,
	}
	title := strings.Join(strings.Fields(i.Entry.Prompt), " ")
	info := humanize.Time(time.Unix(i.Entry.CreatedAt, 0))
	return renderItem(styles, title, info, i.focused, width, i.cache, &i.m)
}

// timelineItems converts entries into list items.
func timelineItems(t *styles.Styles, entries []TimelineEntry) []list.FilterableItem {
	items := make([]list.FilterableItem, len(entries))
	for i, entry := range entries {
		items[i] = &TimelineItem{Versioned: list.NewVersioned(), Entry: entry, t: t}
	}
	return items
}
