package ui

import (
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Column describes one table column. A Width of 0 makes the column flexible:
// leftover horizontal space is shared between all flexible columns.
type Column struct {
	Title    string
	Width    int
	MinWidth int
	Right    bool
	// Less compares two rows for sorting on this column. When nil, the
	// column's cells are compared as lowercase strings.
	Less func(a, b Row) bool
}

// Row is one table line. ID must be stable across refreshes: it is what marks
// and the cursor position are restored against.
type Row struct {
	ID     string
	Cells  []string
	Faint  bool
	Danger bool
	// Data carries the domain object behind the row.
	Data any
}

// Table is a scrollable, filterable, markable list. It owns no terminal state;
// the parent model calls View with the space it may occupy.
type Table struct {
	Columns []Column
	rows    []Row // every row, unfiltered
	view    []Row // rows after filtering and sorting

	cursor  int
	offset  int
	height  int // data rows visible in the last render
	marks   map[string]bool
	filter  string
	sortCol int
	sortAsc bool
}

// NewTable builds a table over the given columns, sorted ascending by the
// first one.
func NewTable(cols []Column) *Table {
	return &Table{Columns: cols, marks: map[string]bool{}, sortAsc: true}
}

// SetRows replaces the table contents, preserving the cursor's row identity
// and any marks that still refer to existing rows. That is what lets a
// background refresh repaint the table under the user's cursor without moving
// it.
func (t *Table) SetRows(rows []Row) {
	var cursorID string
	if r, ok := t.Current(); ok {
		cursorID = r.ID
	}
	t.rows = rows

	if len(t.marks) > 0 {
		alive := make(map[string]bool, len(rows))
		for _, r := range rows {
			alive[r.ID] = true
		}
		for id := range t.marks {
			if !alive[id] {
				delete(t.marks, id)
			}
		}
	}

	t.reindex()
	if cursorID != "" {
		for i, r := range t.view {
			if r.ID == cursorID {
				t.cursor = i
				t.clampOffset()
				return
			}
		}
	}
	t.clampCursor()
}

// UpsertRow replaces the row with the same ID, or appends it. Used by the
// background enrichers, which fill rows in as answers arrive.
func (t *Table) UpsertRow(row Row) {
	for i := range t.rows {
		if t.rows[i].ID == row.ID {
			t.rows[i] = row
			t.SetRows(t.rows)
			return
		}
	}
	t.SetRows(append(t.rows, row))
}

// reindex recomputes the filtered, sorted view.
func (t *Table) reindex() {
	t.view = t.view[:0]
	match := matcher(t.filter)
	for _, r := range t.rows {
		if match == nil || match(r) {
			t.view = append(t.view, r)
		}
	}
	t.sortView()
	t.clampCursor()
}

// matcher compiles a filter expression. A filter is treated as a regular
// expression when it compiles as one, and as a plain case-insensitive
// substring otherwise, so both "^v1\." and "alpine" do what a user expects.
func matcher(filter string) func(Row) bool {
	if filter == "" {
		return nil
	}
	if re, err := regexp.Compile("(?i)" + filter); err == nil {
		return func(r Row) bool {
			for _, c := range r.Cells {
				if re.MatchString(c) {
					return true
				}
			}
			return false
		}
	}
	needle := strings.ToLower(filter)
	return func(r Row) bool {
		for _, c := range r.Cells {
			if strings.Contains(strings.ToLower(c), needle) {
				return true
			}
		}
		return false
	}
}

func (t *Table) sortView() {
	if t.sortCol >= len(t.Columns) {
		t.sortCol = 0
	}
	col := t.Columns[t.sortCol]
	less := col.Less
	if less == nil {
		idx := t.sortCol
		less = func(a, b Row) bool {
			return strings.ToLower(cell(a, idx)) < strings.ToLower(cell(b, idx))
		}
	}
	sort.SliceStable(t.view, func(i, j int) bool {
		if t.sortAsc {
			return less(t.view[i], t.view[j])
		}
		return less(t.view[j], t.view[i])
	})
}

func cell(r Row, i int) string {
	if i < 0 || i >= len(r.Cells) {
		return ""
	}
	return r.Cells[i]
}

// SetFilter applies a filter expression and re-runs the view.
func (t *Table) SetFilter(f string) {
	t.filter = f
	t.reindex()
	t.cursor = 0
	t.offset = 0
}

// Filter returns the active filter expression.
func (t *Table) Filter() string { return t.filter }

// SortBy sorts on a column, flipping the direction when it is already active.
func (t *Table) SortBy(col int) {
	if col < 0 || col >= len(t.Columns) {
		return
	}
	if t.sortCol == col {
		t.sortAsc = !t.sortAsc
	} else {
		t.sortCol, t.sortAsc = col, true
	}
	t.sortView()
}

// CycleSort moves the sort to the next column.
func (t *Table) CycleSort() { t.SortBy((t.sortCol + 1) % max(1, len(t.Columns))) }

// SortLabel describes the active sort for the status line.
func (t *Table) SortLabel() string {
	if t.sortCol >= len(t.Columns) {
		return ""
	}
	arrow := "↑"
	if !t.sortAsc {
		arrow = "↓"
	}
	return t.Columns[t.sortCol].Title + arrow
}

// Current returns the row under the cursor.
func (t *Table) Current() (Row, bool) {
	if t.cursor < 0 || t.cursor >= len(t.view) {
		return Row{}, false
	}
	return t.view[t.cursor], true
}

// Selection returns the marked rows, or the row under the cursor when nothing
// is marked -- the "operate on what I'm pointing at" convention.
func (t *Table) Selection() []Row {
	if len(t.marks) == 0 {
		if r, ok := t.Current(); ok {
			return []Row{r}
		}
		return nil
	}
	out := make([]Row, 0, len(t.marks))
	for _, r := range t.view {
		if t.marks[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// ToggleMark flips the mark on the current row and advances, so a run of rows
// can be marked by holding space.
func (t *Table) ToggleMark() {
	r, ok := t.Current()
	if !ok {
		return
	}
	if t.marks[r.ID] {
		delete(t.marks, r.ID)
	} else {
		t.marks[r.ID] = true
	}
	t.MoveBy(1)
}

// ClearMarks drops every mark.
func (t *Table) ClearMarks() { t.marks = map[string]bool{} }

// MarkCount reports how many rows are marked.
func (t *Table) MarkCount() int { return len(t.marks) }

// Len is the number of rows after filtering; Total is the number before.
func (t *Table) Len() int   { return len(t.view) }
func (t *Table) Total() int { return len(t.rows) }

// Rows returns every row, ignoring the filter.
func (t *Table) Rows() []Row { return t.rows }

// MoveBy moves the cursor by delta rows.
func (t *Table) MoveBy(delta int) {
	t.cursor += delta
	t.clampCursor()
	t.clampOffset()
}

// MoveTo jumps the cursor to an absolute index.
func (t *Table) MoveTo(i int) {
	t.cursor = i
	t.clampCursor()
	t.clampOffset()
}

// PageBy moves the cursor by whole screens.
func (t *Table) PageBy(pages int) { t.MoveBy(pages * max(1, t.height)) }

// Top and Bottom jump to the ends of the list.
func (t *Table) Top()    { t.MoveTo(0) }
func (t *Table) Bottom() { t.MoveTo(len(t.view) - 1) }

func (t *Table) clampCursor() {
	if t.cursor >= len(t.view) {
		t.cursor = len(t.view) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
}

func (t *Table) clampOffset() {
	if t.height <= 0 {
		return
	}
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+t.height {
		t.offset = t.cursor - t.height + 1
	}
	if maxOff := len(t.view) - t.height; t.offset > maxOff {
		t.offset = maxOff
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

// layout resolves column widths for the available width: fixed columns keep
// their size, the rest share what is left, never dropping below MinWidth.
func (t *Table) layout(width int) []int {
	const gap = 1
	widths := make([]int, len(t.Columns))
	fixed, flex := 0, 0
	for i, c := range t.Columns {
		if c.Width > 0 {
			widths[i] = c.Width
			fixed += c.Width
		} else {
			flex++
		}
	}
	gaps := gap * max(0, len(t.Columns)-1)
	// The mark gutter is one cell wide.
	avail := width - fixed - gaps - 2
	if flex > 0 {
		share := avail / flex
		extra := avail % flex
		for i, c := range t.Columns {
			if c.Width > 0 {
				continue
			}
			w := share
			if extra > 0 {
				w++
				extra--
			}
			if w < c.MinWidth {
				w = c.MinWidth
			}
			widths[i] = w
		}
	}
	// If we overshot (many columns, narrow terminal), shrink from the right
	// until the row fits rather than letting lines wrap.
	total := gaps + 2
	for _, w := range widths {
		total += w
	}
	for i := len(widths) - 1; i >= 0 && total > width; i-- {
		floor := t.Columns[i].MinWidth
		if floor <= 0 {
			floor = 3
		}
		if widths[i] > floor {
			shrink := min(widths[i]-floor, total-width)
			widths[i] -= shrink
			total -= shrink
		}
	}
	return widths
}

// View renders the table into width x height cells, including the header row.
func (t *Table) View(width, height int) string {
	if width < 8 || height < 2 {
		return ""
	}
	widths := t.layout(width)
	t.height = height - 1 // one line goes to the header
	t.clampOffset()

	var b strings.Builder

	// Header.
	b.WriteString("  ")
	for i, c := range t.Columns {
		title := c.Title
		if i == t.sortCol {
			arrow := "↑"
			if !t.sortAsc {
				arrow = "↓"
			}
			title += arrow
		}
		b.WriteString(styleColumn.Render(align(title, widths[i], c.Right)))
		if i < len(t.Columns)-1 {
			b.WriteByte(' ')
		}
	}
	b.WriteByte('\n')

	// Rows.
	drawn := 0
	for i := t.offset; i < len(t.view) && drawn < t.height; i++ {
		r := t.view[i]
		var line strings.Builder
		gutter := " "
		if t.marks[r.ID] {
			gutter = "●"
		}
		line.WriteString(gutter + " ")
		for j, c := range t.Columns {
			line.WriteString(align(cell(r, j), widths[j], c.Right))
			if j < len(t.Columns)-1 {
				line.WriteByte(' ')
			}
		}
		text := pad(line.String(), width)

		switch {
		case i == t.cursor:
			b.WriteString(styleSelected.Render(text))
		case t.marks[r.ID]:
			b.WriteString(styleMarked.Render(text))
		case r.Danger:
			b.WriteString(styleDanger.Render(text))
		case r.Faint:
			b.WriteString(styleRowFaint.Render(text))
		default:
			b.WriteString(styleRow.Render(text))
		}
		b.WriteByte('\n')
		drawn++
	}

	if drawn == 0 {
		msg := "no repositories or tags to show"
		if t.filter != "" {
			msg = "nothing matches filter /" + t.filter
		} else if len(t.rows) > 0 {
			msg = "loading…"
		}
		b.WriteString(styleMutedT.Render("  " + msg))
		b.WriteByte('\n')
		drawn++
	}

	// Pad out so the surrounding box keeps a constant height.
	for ; drawn < t.height; drawn++ {
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ScrollHint renders the "12 of 240" position indicator for the status line.
func (t *Table) ScrollHint() string {
	if len(t.view) == 0 {
		return "0/0"
	}
	return itoa(t.cursor+1) + "/" + itoa(len(t.view))
}

func align(s string, width int, right bool) string {
	if !right {
		return pad(s, width)
	}
	s = truncate(s, width)
	if gap := width - lipgloss.Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
