package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// headerHeight is the fixed height of the top chrome (info block, key hints
// and logo), matching the height of the ASCII logo.
const headerHeight = 4

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// bodyHeight is the number of content lines the main box can show, excluding
// its own border.
func (m *Model) bodyHeight() int {
	// header + box top + box bottom + status line
	h := m.height - headerHeight - 3
	if h < 3 {
		return 3
	}
	return h
}

// View renders the whole screen.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting…"
	}

	body := m.renderBody()
	if m.mode == modeConfirm && m.confirm != nil {
		body = m.renderConfirm()
	}

	return strings.Join([]string{
		m.renderHeader(),
		body,
		m.renderStatus(),
	}, "\n")
}

// --- header --------------------------------------------------------------

func (m *Model) renderHeader() string {
	info := m.renderInfo()
	blocks := []string{info}

	remaining := m.width - lipgloss.Width(info)

	// The logo is the first thing to go on a narrow terminal.
	logo := ""
	if m.width >= 92 {
		logo = m.renderLogo()
		remaining -= lipgloss.Width(logo) + 2
	}
	if hints := m.renderHints(remaining); hints != "" {
		blocks = append(blocks, hints)
	}
	if logo != "" {
		row := lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
		gap := m.width - lipgloss.Width(row) - lipgloss.Width(logo)
		if gap < 1 {
			gap = 1
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, row, strings.Repeat(" ", gap), logo)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

// renderInfo is the k9s-style context block in the top-left corner.
func (m *Model) renderInfo() string {
	user := m.cfg.Username
	if user == "" {
		user = "anonymous"
	}

	view := "repositories"
	switch m.screen {
	case screenTags:
		view = "tags · " + m.repo
	case screenDetail:
		view = "image · " + m.detailTitle
	case screenHelp:
		view = "help"
	}

	mode := styleOK.Render("read-write")
	switch {
	case m.cfg.DryRun:
		mode = styleWarn.Render("dry-run — no deletes are sent")
	case m.cfg.ReadOnly:
		mode = styleWarn.Render("read-only")
	}

	// Size the value column to its contents rather than a fixed width, so a
	// short registry address leaves more room for the key hints beside it.
	const labelW = 10
	values := []string{
		styleValue.Render(m.client.Address()),
		styleValue.Render(user),
		styleValue.Render(view),
		mode,
	}
	valueW := 16
	for _, v := range values {
		if w := lipgloss.Width(v); w > valueW {
			valueW = w
		}
	}
	if valueW > 34 {
		valueW = 34
	}

	lines := make([]string, 0, len(values))
	for i, label := range []string{"Registry:", "User:", "View:", "Mode:"} {
		lines = append(lines, styleLabel.Render(pad(label, labelW))+pad(values[i], valueW)+"  ")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderLogo() string {
	out := make([]string, len(asciiLogo))
	for i, l := range asciiLogo {
		out[i] = styleLogo.Render(l)
	}
	return strings.Join(out, "\n")
}

// hint is one key binding shown in the header.
type hint struct{ key, desc string }

// renderHints lays the screen's key bindings out in as many columns as fit.
// When they do not all fit, the screen-specific ones are dropped first: help
// and quit have to stay reachable on any terminal.
func (m *Model) renderHints(width int) string {
	specific, common := m.screenHints(), commonHints()
	if width < 22 {
		return ""
	}

	const colWidth = 22
	cols := width / colWidth
	if cols < 1 {
		cols = 1
	}
	capacity := cols * headerHeight
	if len(specific)+len(common) > capacity {
		specific = specific[:max(0, capacity-len(common))]
	}
	hints := append(specific, common...)
	if len(hints) == 0 {
		return ""
	}

	rows := headerHeight
	if need := (len(hints) + cols - 1) / cols; need < rows {
		rows = need
	}

	lines := make([]string, headerHeight)
	for r := 0; r < headerHeight; r++ {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			i := c*rows + r
			if r >= rows || i >= len(hints) {
				b.WriteString(strings.Repeat(" ", colWidth))
				continue
			}
			label := styleKey.Render("<"+hints[i].key+">") + " " + styleMutedT.Render(hints[i].desc)
			b.WriteString(pad(label, colWidth))
		}
		lines[r] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(lines, "\n")
}

// commonHints are shown on every screen, and are the last to be dropped.
func commonHints() []hint {
	return []hint{
		{"?", "help"},
		{"q", "quit"},
	}
}

// screenHints are the bindings specific to the current screen, most useful
// first, since the tail is what gets dropped on a narrow terminal.
func (m *Model) screenHints() []hint {
	switch m.screen {
	case screenRepos:
		return []hint{
			{"enter", "show tags"},
			{"ctrl-d", "delete repo"},
			{"/", "filter"},
			{"y", "yank name"},
			{"r", "refresh"},
			{"s", "sort"},
			{":", "command"},
		}
	case screenTags:
		return []hint{
			{"enter", "describe"},
			{"ctrl-d", "delete image"},
			{"space", "mark"},
			{"esc", "back"},
			{"/", "filter"},
			{"y", "yank ref"},
			{"r", "refresh"},
			{"s", "sort"},
			{"ctrl-\\", "clear marks"},
			{":", "command"},
		}
	case screenDetail:
		return []hint{
			{"j/k", "scroll"},
			{"g/G", "top/bottom"},
			{"y", "yank ref"},
			{"esc", "back"},
		}
	default:
		return []hint{{"j/k", "scroll"}, {"esc", "back"}}
	}
}

// --- body ----------------------------------------------------------------

func (m *Model) renderBody() string {
	h := m.bodyHeight()
	inner := m.width - 2

	switch m.screen {
	case screenHelp:
		// Rendered once per frame and kept, so the key handler knows how far
		// the text can scroll.
		m.helpLines = strings.Split(strings.TrimRight(helpContent(m.cfg.Version), "\n"), "\n")
		m.helpOffset = clampScroll(m.helpOffset, len(m.helpLines), h)
		return m.renderScrollable("Help", m.helpLines, m.helpOffset, h)

	case screenDetail:
		if m.detailStream.loading && len(m.detailLines) == 0 {
			return drawBox(m.detailTitle, centered(m.spinner()+" loading manifest…", inner, h), m.width, h)
		}
		return m.renderScrollable(m.detailTitle, m.detailLines, m.detailOffset, h)

	default:
		t := m.currentTable()
		title := m.boxTitle()
		if m.fatal != "" && t.Total() == 0 {
			return drawBox(title, centered(styleDanger.Render(m.fatal), inner, h), m.width, h)
		}
		if t.Total() == 0 && m.busy() {
			return drawBox(title, centered(m.spinner()+" loading…", inner, h), m.width, h)
		}
		return drawBox(title, t.View(inner, h), m.width, h)
	}
}

// renderScrollable draws a window onto a block of text, noting the position in
// the box title when the text does not fit.
func (m *Model) renderScrollable(title string, lines []string, offset, height int) string {
	visible := lines
	if offset < len(visible) {
		visible = visible[offset:]
	} else {
		visible = nil
	}
	if len(visible) > height {
		visible = visible[:height]
	}
	if len(lines) > height {
		title = fmt.Sprintf("%s  (%d-%d/%d)", title, offset+1, offset+len(visible), len(lines))
	}
	return drawBox(title, strings.Join(visible, "\n"), m.width, height)
}

// boxTitle mirrors k9s: the resource name, the count, and the active filter.
func (m *Model) boxTitle() string {
	t := m.currentTable()
	name := "Repositories"
	if m.screen == screenTags {
		name = "Images · " + m.repo
	}
	title := fmt.Sprintf("%s(%d)", name, t.Total())
	if f := t.Filter(); f != "" {
		title += styleWarn.Render(fmt.Sprintf("[/%s → %d]", f, t.Len()))
	}
	if n := t.MarkCount(); n > 0 {
		title += styleMarked.Render(fmt.Sprintf("[%d marked]", n))
	}
	return title
}

// drawBox frames content in a rounded box whose top border carries the title.
func drawBox(title, content string, width, height int) string {
	if width < 6 {
		return content
	}
	inner := width - 2

	titleText := " " + title + " "
	fill := inner - lipgloss.Width(titleText) - 1
	if fill < 0 {
		titleText = " " + truncate(title, inner-4) + " "
		fill = inner - lipgloss.Width(titleText) - 1
	}
	if fill < 0 {
		fill = 0
	}

	var b strings.Builder
	b.WriteString(styleBox.GetBorderStyle().TopLeft)
	b.WriteString(styleBox.GetBorderStyle().Top)
	b.WriteString(styleTitle.Render(titleText))
	b.WriteString(strings.Repeat(styleBox.GetBorderStyle().Top, fill))
	b.WriteString(styleBox.GetBorderStyle().TopRight)
	top := b.String()

	side := styleBox.GetBorderStyle().Left
	lines := strings.Split(content, "\n")
	var body strings.Builder
	for i := 0; i < height; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		body.WriteString(side + pad(line, inner) + side + "\n")
	}

	bottom := styleBox.GetBorderStyle().BottomLeft +
		strings.Repeat(styleBox.GetBorderStyle().Bottom, inner) +
		styleBox.GetBorderStyle().BottomRight

	return top + "\n" + body.String() + bottom
}

// centered places a single line in the middle of a content area.
func centered(s string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, s)
}

func (m *Model) spinner() string {
	return styleWarn.Render(spinnerFrames[m.frame%len(spinnerFrames)])
}

// --- confirmation dialog -------------------------------------------------

func (m *Model) renderConfirm() string {
	c := m.confirm
	h := m.bodyHeight()

	parts := []string{styleDialogTitle.Render(c.title), ""}
	parts = append(parts, c.lines...)
	parts = append(parts, "")

	if c.requireText != "" {
		parts = append(parts,
			stylePrompt.Render("› ")+c.input.View(),
			"",
			styleMutedT.Render("enter confirm · esc cancel"))
	} else {
		parts = append(parts,
			styleKey.Render("<y>")+styleMutedT.Render(" or ")+
				styleKey.Render("<enter>")+styleMutedT.Render(" delete   ")+
				styleKey.Render("<n>")+styleMutedT.Render(" or ")+
				styleKey.Render("<esc>")+styleMutedT.Render(" cancel"))
	}

	dialog := styleDialog.Render(strings.Join(parts, "\n"))
	return lipgloss.Place(m.width, h+2, lipgloss.Center, lipgloss.Center, dialog)
}

// --- status line ---------------------------------------------------------

func (m *Model) renderStatus() string {
	left := m.renderStatusLeft()
	right := m.renderStatusRight()

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Sacrifice the left side, which is the part that can be re-read.
		left = truncate(left, max(0, m.width-lipgloss.Width(right)-1))
		gap = max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) renderStatusLeft() string {
	switch m.mode {
	case modeFilter:
		return stylePrompt.Render("/") + m.filterInput.View()
	case modeCommand:
		return stylePrompt.Render(":") + m.cmdInput.View()
	}

	prefix := ""
	if m.busy() {
		prefix = m.spinner() + " "
	}

	style := styleMutedT
	switch m.level {
	case statusOK:
		style = styleOK
	case statusWarn:
		style = styleWarn
	case statusError:
		style = styleDanger
	}
	return prefix + style.Render(m.status)
}

func (m *Model) renderStatusRight() string {
	var parts []string
	if t := m.currentTable(); t != nil {
		if n := t.MarkCount(); n > 0 {
			parts = append(parts, styleMarked.Render(fmt.Sprintf("%d marked", n)))
		}
		parts = append(parts, styleMutedT.Render(t.SortLabel()), styleMutedT.Render(t.ScrollHint()))
	}
	if m.cfg.DryRun {
		parts = append([]string{styleWarn.Render("DRY-RUN")}, parts...)
	} else if m.cfg.ReadOnly {
		parts = append([]string{styleWarn.Render("READ-ONLY")}, parts...)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, styleFaintSep) + " "
}

// styleFaintSep separates status-line segments.
var styleFaintSep = styleMutedT.Render(" · ")
