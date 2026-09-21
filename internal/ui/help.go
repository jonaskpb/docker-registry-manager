package ui

import "strings"

// helpSection is one titled group of key bindings on the help screen.
type helpSection struct {
	title string
	keys  [][2]string
}

var helpSections = []helpSection{
	{"Navigation", [][2]string{
		{"j / ↓, k / ↑", "move down / up"},
		{"g / G", "jump to first / last row"},
		{"ctrl-f / ctrl-b", "page down / up"},
		{"enter, d", "drill down (repository → tags → image)"},
		{"esc", "go back one level, or clear the filter"},
		{":", "command prompt"},
		{"q, ctrl-c", "quit"},
	}},
	{"Finding things", [][2]string{
		{"/", "filter rows — a regular expression, or plain text"},
		{"esc", "clear the filter"},
		{"s", "cycle the sort column"},
		{"1…9", "sort by the n-th column (press again to reverse)"},
		{":<repository>", "jump straight to a repository"},
	}},
	{"Deleting images", [][2]string{
		{"space", "mark the row and move on"},
		{"ctrl-\\, M", "clear all marks"},
		{"ctrl-d", "delete the marked images, or the one under the cursor"},
		{"ctrl-d (repo list)", "delete every image in the repository"},
		{"y", "copy the image reference to the clipboard"},
		{"r, ctrl-r", "refresh from the registry"},
	}},
	{"Commands", [][2]string{
		{":repos", "back to the repository list"},
		{":help", "this screen"},
		{":refresh", "reload the current view"},
		{":quit", "quit"},
	}},
}

// helpContent renders the help screen body.
func helpContent(version string) string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("  drm — docker registry manager"))
	if version != "" {
		b.WriteString(styleMutedT.Render("  " + version))
	}
	b.WriteString("\n")

	for _, section := range helpSections {
		b.WriteString("\n  " + styleTitle.Render(section.title) + "\n")
		for _, k := range section.keys {
			b.WriteString("    " + styleKey.Render(pad(k[0], 20)) + " " + styleValue.Render(k[1]) + "\n")
		}
	}

	b.WriteString("\n  " + styleTitle.Render("About deleting") + "\n")
	for _, line := range []string{
		"A registry deletes by manifest digest, never by tag. Every tag pointing at",
		"the same digest therefore disappears together — the confirmation says when",
		"that is about to happen.",
		"",
		"Deleting frees no disk space on its own: the registry only reclaims blobs",
		"when its garbage collector runs (registry garbage-collect /etc/docker/registry/config.yml).",
		"",
		"Registries reject deletes unless they were started with deletion enabled",
		"(REGISTRY_STORAGE_DELETE_ENABLED=true).",
	} {
		b.WriteString("    " + styleMutedT.Render(line) + "\n")
	}
	return b.String()
}
