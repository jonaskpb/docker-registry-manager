package ui

import (
	"strings"
	"testing"
)

func testRows() []Row {
	return []Row{
		{ID: "alpine", Cells: []string{"alpine", "3"}},
		{ID: "nginx", Cells: []string{"nginx", "12"}},
		{ID: "team/api", Cells: []string{"team/api", "7"}},
	}
}

func TestTableFilter(t *testing.T) {
	tbl := NewTable([]Column{{Title: "NAME"}, {Title: "TAGS", Width: 6}})
	tbl.SetRows(testRows())

	if tbl.Len() != 3 {
		t.Fatalf("unfiltered length = %d, want 3", tbl.Len())
	}

	// Plain substring, case-insensitive.
	tbl.SetFilter("NGIN")
	if tbl.Len() != 1 {
		t.Errorf("filter NGIN matched %d rows, want 1", tbl.Len())
	}

	// A regular expression, since that is what a k9s user will reach for.
	tbl.SetFilter("^team/")
	if tbl.Len() != 1 {
		t.Errorf("filter ^team/ matched %d rows, want 1", tbl.Len())
	}

	// An expression that cannot compile falls back to a literal search
	// instead of erroring at the user.
	tbl.SetFilter("api[")
	if tbl.Len() != 0 {
		t.Errorf("unmatched literal filter returned %d rows, want 0", tbl.Len())
	}

	tbl.SetFilter("")
	if tbl.Len() != 3 {
		t.Errorf("cleared filter left %d rows, want 3", tbl.Len())
	}
}

func TestTableSort(t *testing.T) {
	tbl := NewTable([]Column{
		{Title: "NAME"},
		{Title: "TAGS", Width: 6, Less: func(a, b Row) bool { return len(a.Cells[1]) < len(b.Cells[1]) }},
	})
	tbl.SetRows(testRows())

	first, _ := tbl.Current()
	if first.ID != "alpine" {
		t.Errorf("default sort put %s first, want alpine", first.ID)
	}

	// Sorting the same column twice reverses it.
	tbl.SortBy(0)
	tbl.Bottom()
	last, _ := tbl.Current()
	if last.ID != "alpine" {
		t.Errorf("after reversing, last row = %s, want alpine", last.ID)
	}
}

// A background refresh repaints the table; the cursor must stay on the row the
// user was pointing at, not jump back to the top.
func TestTableKeepsCursorAcrossRefresh(t *testing.T) {
	tbl := NewTable([]Column{{Title: "NAME"}, {Title: "TAGS", Width: 6}})
	tbl.SetRows(testRows())
	tbl.View(60, 10) // establish a viewport height
	tbl.MoveBy(2)

	before, _ := tbl.Current()
	if before.ID != "team/api" {
		t.Fatalf("cursor on %s, want team/api", before.ID)
	}

	// Same rows, new values -- what the enrichment stream produces.
	refreshed := testRows()
	refreshed[2].Cells[1] = "9"
	tbl.SetRows(refreshed)

	after, _ := tbl.Current()
	if after.ID != "team/api" {
		t.Errorf("cursor moved to %s after refresh, want team/api", after.ID)
	}
	if after.Cells[1] != "9" {
		t.Errorf("row not updated: tags = %s, want 9", after.Cells[1])
	}
}

func TestTableMarksDropDeletedRows(t *testing.T) {
	tbl := NewTable([]Column{{Title: "NAME"}, {Title: "TAGS", Width: 6}})
	tbl.SetRows(testRows())
	tbl.View(60, 10)

	tbl.ToggleMark() // marks alpine, advances to nginx
	tbl.ToggleMark() // marks nginx
	if tbl.MarkCount() != 2 {
		t.Fatalf("mark count = %d, want 2", tbl.MarkCount())
	}

	// nginx is deleted from the registry and disappears on refresh.
	tbl.SetRows([]Row{{ID: "alpine", Cells: []string{"alpine", "3"}}})
	if tbl.MarkCount() != 1 {
		t.Errorf("mark count = %d after a marked row vanished, want 1", tbl.MarkCount())
	}
}

// With nothing marked, an action applies to the row under the cursor.
func TestTableSelectionFallsBackToCursor(t *testing.T) {
	tbl := NewTable([]Column{{Title: "NAME"}, {Title: "TAGS", Width: 6}})
	tbl.SetRows(testRows())
	tbl.View(60, 10)

	sel := tbl.Selection()
	if len(sel) != 1 || sel[0].ID != "alpine" {
		t.Fatalf("selection = %v, want [alpine]", sel)
	}

	tbl.ToggleMark()
	tbl.MoveTo(2)
	tbl.ToggleMark()
	sel = tbl.Selection()
	if len(sel) != 2 {
		t.Errorf("selection = %d rows, want the 2 marked ones", len(sel))
	}
}

// Rendering must never exceed the width it is given, or the box border and
// every line below it would wrap.
func TestTableViewRespectsWidth(t *testing.T) {
	tbl := NewTable(tagColumns())
	tbl.SetRows([]Row{{
		ID:    "a-very-long-tag-name-that-will-not-fit-in-any-column",
		Cells: []string{"a-very-long-tag-name-that-will-not-fit-in-any-column", "sha256abcdef", "123 MiB", "3d", "linux/amd64,linux/arm64,windows/amd64", "12"},
	}})

	for _, width := range []int{40, 60, 80, 120, 200} {
		out := tbl.View(width, 6)
		for i, line := range strings.Split(out, "\n") {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: line %d is %d cells wide", width, i, w)
			}
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:                "-",
		512:              "512 B",
		1024:             "1.00 KiB",
		1536:             "1.50 KiB",
		10 * 1024:        "10.0 KiB",
		100 * 1024:       "100 KiB",
		5 * 1024 * 1024:  "5.00 MiB",
		1024 * 1024 * 3:  "3.00 MiB",
		1 << 30:          "1.00 GiB",
		(1 << 30) * 1536: "1.50 TiB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"alpine", 10, "alpine"},
		{"alpine", 6, "alpine"},
		{"alpine", 5, "alpi…"},
		{"alpine", 1, "…"},
		{"alpine", 0, ""},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.width); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
	}
}
