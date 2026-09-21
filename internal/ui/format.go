package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// humanSize renders a byte count the way registry UIs do: three significant
// digits with a binary unit.
func humanSize(n int64) string {
	if n <= 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	units := [...]string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	switch {
	case val >= 100:
		return fmt.Sprintf("%.0f %s", val, units[exp])
	case val >= 10:
		return fmt.Sprintf("%.1f %s", val, units[exp])
	default:
		return fmt.Sprintf("%.2f %s", val, units[exp])
	}
}

// humanAge renders a timestamp as a compact age, e.g. 3d or 14m, matching the
// AGE column of kubectl and k9s.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

// truncate shortens s to width, marking the cut with an ellipsis. Width is
// measured in terminal cells so wide runes do not break column alignment.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var (
		b     strings.Builder
		limit = width - 1
		used  int
	)
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > limit {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// pad right-pads s to exactly width cells, truncating when it overflows.
func pad(s string, width int) string {
	s = truncate(s, width)
	if gap := width - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// joinLimit renders a string slice, collapsing the tail into "+N" once it
// would not fit the column.
func joinLimit(items []string, width int) string {
	if len(items) == 0 {
		return "-"
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		next := out + "," + items[i]
		if lipgloss.Width(next)+4 > width {
			return fmt.Sprintf("%s +%d", out, len(items)-i)
		}
		out = next
	}
	return out
}
