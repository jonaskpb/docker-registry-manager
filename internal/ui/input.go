package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// input is a single-line text field: enough editing for a filter expression or
// a confirmation phrase, without pulling in a full text-input component.
type input struct {
	value       []rune
	cursor      int
	Placeholder string
}

// SetValue replaces the contents and parks the cursor at the end.
func (i *input) SetValue(s string) {
	i.value = []rune(s)
	i.cursor = len(i.value)
}

// Value returns the current text.
func (i *input) Value() string { return string(i.value) }

// Reset empties the field.
func (i *input) Reset() { i.value, i.cursor = nil, 0 }

// Update applies one keypress, reporting whether it was consumed. Keys the
// field does not handle (enter, esc, ...) are left to the caller.
func (i *input) Update(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes:
		i.insert(msg.Runes)
		return true
	case tea.KeySpace:
		i.insert([]rune{' '})
		return true
	case tea.KeyBackspace:
		if i.cursor > 0 {
			i.value = append(i.value[:i.cursor-1], i.value[i.cursor:]...)
			i.cursor--
		}
		return true
	case tea.KeyDelete:
		if i.cursor < len(i.value) {
			i.value = append(i.value[:i.cursor], i.value[i.cursor+1:]...)
		}
		return true
	case tea.KeyLeft:
		if i.cursor > 0 {
			i.cursor--
		}
		return true
	case tea.KeyRight:
		if i.cursor < len(i.value) {
			i.cursor++
		}
		return true
	case tea.KeyHome, tea.KeyCtrlA:
		i.cursor = 0
		return true
	case tea.KeyEnd, tea.KeyCtrlE:
		i.cursor = len(i.value)
		return true
	case tea.KeyCtrlU:
		i.value, i.cursor = i.value[i.cursor:], 0
		return true
	case tea.KeyCtrlW:
		i.deleteWord()
		return true
	}
	return false
}

func (i *input) insert(runes []rune) {
	out := make([]rune, 0, len(i.value)+len(runes))
	out = append(out, i.value[:i.cursor]...)
	out = append(out, runes...)
	out = append(out, i.value[i.cursor:]...)
	i.value = out
	i.cursor += len(runes)
}

// deleteWord removes the whitespace-delimited word before the cursor.
func (i *input) deleteWord() {
	end := i.cursor
	for end > 0 && i.value[end-1] == ' ' {
		end--
	}
	start := end
	for start > 0 && i.value[start-1] != ' ' {
		start--
	}
	i.value = append(i.value[:start], i.value[i.cursor:]...)
	i.cursor = start
}

// View renders the field with a block cursor, or the placeholder when empty.
func (i *input) View() string {
	if len(i.value) == 0 {
		return styleSelected.Render(" ") + styleMutedT.Render(i.Placeholder)
	}
	var b strings.Builder
	b.WriteString(styleValue.Render(string(i.value[:i.cursor])))
	if i.cursor < len(i.value) {
		b.WriteString(styleSelected.Render(string(i.value[i.cursor])))
		b.WriteString(styleValue.Render(string(i.value[i.cursor+1:])))
	} else {
		b.WriteString(styleSelected.Render(" "))
	}
	return b.String()
}
