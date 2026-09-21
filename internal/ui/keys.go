package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleKey dispatches a keypress to the handler for the active input mode.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeCommand:
		return m.handleCommandKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	}
	return m.handleNormalKey(msg)
}

func (m *Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Keys that mean the same thing everywhere.
	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		if m.screen == screenHelp {
			return m, m.goBack()
		}
		m.screen, m.helpOffset = screenHelp, 0
		return m, nil
	case ":":
		m.mode = modeCommand
		m.cmdInput.Reset()
		return m, nil
	case "esc":
		return m, m.goBack()
	}

	switch m.screen {
	case screenDetail:
		return m.handleDetailKey(key)
	case screenHelp:
		return m.handleHelpKey(key)
	}
	return m.handleTableKey(key)
}

// handleTableKey serves the repository and tag lists, which share navigation.
func (m *Model) handleTableKey(key string) (tea.Model, tea.Cmd) {
	t := m.currentTable()
	if t == nil {
		return m, nil
	}

	var cmd tea.Cmd
	switch key {
	case "j", "down", "ctrl+n":
		t.MoveBy(1)
	case "k", "up", "ctrl+p":
		t.MoveBy(-1)
	case "g", "home":
		t.Top()
	case "G", "end":
		t.Bottom()
	case "ctrl+f", "pgdown":
		t.PageBy(1)
	case "ctrl+b", "pgup":
		t.PageBy(-1)
	case "/":
		m.mode = modeFilter
		m.filterInput.SetValue(t.Filter())
	case "r", "ctrl+r":
		return m, m.refresh()
	case "s":
		t.CycleSort()
		cmd = m.flash("sorted by "+t.SortLabel(), statusInfo, flashShort)
	case " ":
		t.ToggleMark()
	case "ctrl+\\", "M":
		t.ClearMarks()
		cmd = m.flash("marks cleared", statusInfo, flashShort)
	case "enter", "d", "l", "right":
		return m, m.drillDown()
	case "y":
		return m, m.yankSelection()
	case "ctrl+d":
		return m, m.requestDelete()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		t.SortBy(int(key[0] - '1'))
		cmd = m.flash("sorted by "+t.SortLabel(), statusInfo, flashShort)
	}
	return m, cmd
}

func (m *Model) handleDetailKey(key string) (tea.Model, tea.Cmd) {
	page := max(1, m.bodyHeight()-2)
	switch key {
	case "j", "down":
		m.scrollDetail(1)
	case "k", "up":
		m.scrollDetail(-1)
	case "ctrl+f", "pgdown", " ":
		m.scrollDetail(page)
	case "ctrl+b", "pgup":
		m.scrollDetail(-page)
	case "g", "home":
		m.detailOffset = 0
	case "G", "end":
		m.scrollDetail(len(m.detailLines))
	case "y":
		return m, yank(m.detailRef, m.detailRef)
	case "r", "ctrl+r":
		return m, m.refresh()
	}
	return m, nil
}

// handleHelpKey scrolls the help screen. The help text is longer than most
// terminals are tall, so it needs the same navigation as the detail view.
func (m *Model) handleHelpKey(key string) (tea.Model, tea.Cmd) {
	page := max(1, m.bodyHeight()-2)
	switch key {
	case "j", "down":
		m.helpOffset = clampScroll(m.helpOffset+1, len(m.helpLines), m.bodyHeight())
	case "k", "up":
		m.helpOffset = clampScroll(m.helpOffset-1, len(m.helpLines), m.bodyHeight())
	case "ctrl+f", "pgdown", " ":
		m.helpOffset = clampScroll(m.helpOffset+page, len(m.helpLines), m.bodyHeight())
	case "ctrl+b", "pgup":
		m.helpOffset = clampScroll(m.helpOffset-page, len(m.helpLines), m.bodyHeight())
	case "g", "home":
		m.helpOffset = 0
	case "G", "end":
		m.helpOffset = clampScroll(len(m.helpLines), len(m.helpLines), m.bodyHeight())
	}
	return m, nil
}

// clampScroll keeps a scroll offset inside the scrollable range.
func clampScroll(offset, total, height int) int {
	if maxOff := total - height; offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		return 0
	}
	return offset
}

func (m *Model) scrollDetail(delta int) {
	m.detailOffset = clampScroll(m.detailOffset+delta, len(m.detailLines), m.bodyHeight())
}

// goBack pops one level of the navigation stack.
func (m *Model) goBack() tea.Cmd {
	switch m.screen {
	case screenHelp:
		// Returning from help lands wherever the user was; the tag screen is
		// only a valid destination if a repository is open.
		if m.repo != "" {
			m.screen = screenTags
		} else {
			m.screen = screenRepos
		}
	case screenDetail:
		m.detailStream.cancelIfSet()
		m.screen = screenTags
	case screenTags:
		if m.tags.Filter() != "" {
			m.tags.SetFilter("")
			break
		}
		m.tagStream.cancelIfSet()
		m.screen = screenRepos
		m.repo = ""
	case screenRepos:
		if m.repos.Filter() != "" {
			m.repos.SetFilter("")
		}
	}
	m.setStatus(m.defaultStatus(), statusInfo)
	return nil
}

// drillDown opens the next level down from the current selection.
func (m *Model) drillDown() tea.Cmd {
	switch m.screen {
	case screenRepos:
		row, ok := m.repos.Current()
		if !ok {
			return nil
		}
		return m.openRepo(row.ID)
	case screenTags:
		row, ok := m.tags.Current()
		if !ok {
			return nil
		}
		return m.openDetail(m.repo, row.ID)
	}
	return nil
}

// refresh re-reads whatever the current screen shows.
func (m *Model) refresh() tea.Cmd {
	switch m.screen {
	case screenTags:
		return m.openRepo(m.repo)
	case screenDetail:
		return m.openDetail(m.repo, strings.TrimPrefix(m.detailTitle, m.repo+":"))
	default:
		return m.reloadRepos()
	}
}

// yankSelection copies the pullable reference of the current row.
func (m *Model) yankSelection() tea.Cmd {
	switch m.screen {
	case screenRepos:
		row, ok := m.repos.Current()
		if !ok {
			return nil
		}
		ref := m.client.Host() + "/" + row.ID
		return yank(ref, ref)
	case screenTags:
		rows := m.tags.Selection()
		if len(rows) == 0 {
			return nil
		}
		refs := make([]string, 0, len(rows))
		for _, r := range rows {
			refs = append(refs, rowTag(r).Ref(m.client.Host()))
		}
		label := refs[0]
		if len(refs) > 1 {
			label = fmt.Sprintf("%d references", len(refs))
		}
		return yank(strings.Join(refs, "\n"), label)
	}
	return nil
}

// --- filter mode ---------------------------------------------------------

func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.currentTable()
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeNormal
		if t != nil {
			t.SetFilter("")
		}
		return m, nil
	case "enter":
		m.mode = modeNormal
		return m, nil
	}
	if m.filterInput.Update(msg) && t != nil {
		// Filter as the user types, the way k9s does.
		t.SetFilter(m.filterInput.Value())
	}
	return m, nil
}

// --- command mode --------------------------------------------------------

func (m *Model) handleCommandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeNormal
		return m, nil
	case "enter":
		m.mode = modeNormal
		return m, m.runCommand(strings.TrimSpace(m.cmdInput.Value()))
	}
	m.cmdInput.Update(msg)
	return m, nil
}

// runCommand interprets a ":" command: a handful of verbs, and otherwise a
// repository name to jump straight to.
func (m *Model) runCommand(cmd string) tea.Cmd {
	if cmd == "" {
		return nil
	}
	switch strings.ToLower(cmd) {
	case "q", "quit", "exit":
		return tea.Quit
	case "repos", "repo", "r", "root", "catalog":
		m.repo = ""
		m.screen = screenRepos
		m.setStatus(m.defaultStatus(), statusInfo)
		return nil
	case "help", "h", "?":
		m.screen, m.helpOffset = screenHelp, 0
		return nil
	case "refresh":
		return m.refresh()
	}

	// Otherwise: jump to a repository, by exact name or unique prefix.
	var matches []string
	for _, row := range m.repos.Rows() {
		if row.ID == cmd {
			return m.openRepo(cmd)
		}
		if strings.Contains(row.ID, cmd) {
			matches = append(matches, row.ID)
		}
	}
	switch len(matches) {
	case 0:
		return m.flash(fmt.Sprintf("no repository matches %q", cmd), statusWarn, flashShort)
	case 1:
		return m.openRepo(matches[0])
	default:
		// Several candidates: show them filtered rather than guessing.
		m.screen = screenRepos
		m.repos.SetFilter(cmd)
		return m.flash(fmt.Sprintf("%d repositories match %q", len(matches), cmd), statusInfo, flashShort)
	}
}

// --- confirmation mode ---------------------------------------------------

func (m *Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.confirm
	if c == nil {
		m.mode = modeNormal
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c", "n", "N":
		if c.requireText != "" && msg.String() != "esc" && msg.String() != "ctrl+c" {
			break // "n" is a legitimate character to type into the field
		}
		m.mode, m.confirm = modeNormal, nil
		return m, m.flash("cancelled", statusInfo, flashShort)
	case "enter":
		if c.requireText != "" && c.input.Value() != c.requireText {
			return m, m.flash(fmt.Sprintf("type %q exactly to confirm", c.requireText), statusWarn, flashShort)
		}
		run := c.run
		m.mode, m.confirm = modeNormal, nil
		m.status, m.level = "deleting…", statusWarn
		return m, run(m)
	case "y", "Y":
		if c.requireText == "" {
			run := c.run
			m.mode, m.confirm = modeNormal, nil
			m.status, m.level = "deleting…", statusWarn
			return m, run(m)
		}
	}
	if c.requireText != "" {
		c.input.Update(msg)
	}
	return m, nil
}

// cancelIfSet cancels a stream without opening a new generation.
func (s *stream) cancelIfSet() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.loading = false
	s.gen++
}
