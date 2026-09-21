package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
)

// requestDelete opens the confirmation dialog for whatever the current screen
// has selected. Nothing is sent to the registry until the user confirms.
func (m *Model) requestDelete() tea.Cmd {
	if m.cfg.ReadOnly {
		return m.flash("read-only mode — restart without --read-only to delete", statusWarn, flashShort)
	}
	switch m.screen {
	case screenTags:
		return m.confirmDeleteTags()
	case screenRepos:
		return m.confirmPurgeRepo()
	}
	return nil
}

// confirmDeleteTags stages deletion of the marked tags (or the tag under the
// cursor).
func (m *Model) confirmDeleteTags() tea.Cmd {
	selected := m.selectedTags()
	if len(selected) == 0 {
		return nil
	}
	targets := planDeletes(selected, m.allTags())
	if len(targets) == 0 {
		return nil
	}

	lines := make([]string, 0, len(targets)+4)
	lines = append(lines, styleLabel.Render(fmt.Sprintf("Repository %s", m.repo)), "")

	// Warn when a delete takes down tags the user did not select: that is the
	// single most surprising thing about the registry delete API.
	selectedTagNames := map[string]bool{}
	for _, s := range selected {
		selectedTagNames[s.Tag] = true
	}
	collateral := 0

	for _, t := range targets {
		digest := registry.ShortDigest(t.Digest)
		if digest == "" {
			digest = "(resolved on delete)"
		}
		lines = append(lines, fmt.Sprintf("  %s  %s",
			styleDanger.Render(pad(strings.Join(t.Tags, ", "), 34)),
			styleMutedT.Render(digest)))
		for _, tag := range t.Tags {
			if !selectedTagNames[tag] {
				collateral++
			}
		}
	}

	lines = append(lines, "")
	if collateral > 0 {
		warning := fmt.Sprintf("⚠ %s also points at this manifest and will be removed too.",
			plural(collateral, "other tag", "other tags"))
		if collateral > 1 {
			warning = fmt.Sprintf("⚠ %s also point at these manifests and will be removed too.",
				plural(collateral, "other tag", "other tags"))
		}
		lines = append(lines, styleWarn.Render(warning))
	}
	lines = append(lines, styleMutedT.Render("Blob storage is reclaimed only by the registry's garbage collector."))

	title := fmt.Sprintf("Delete %s", plural(len(targets), "manifest", "manifests"))
	if m.cfg.DryRun {
		title = "DRY RUN — " + title
	}

	m.confirm = &confirmState{
		title: title,
		lines: lines,
		run: func(mm *Model) tea.Cmd {
			return runDeletes(mm.deleteCtx(), mm.client, targets, mm.cfg.DryRun)
		},
	}
	m.mode = modeConfirm
	return nil
}

// confirmPurgeRepo stages deletion of every manifest in a repository. Because
// this is unrecoverable and wholesale, it asks the user to type the repository
// name rather than accepting a single keystroke.
func (m *Model) confirmPurgeRepo() tea.Cmd {
	row, ok := m.repos.Current()
	if !ok {
		return nil
	}
	info := rowRepo(row)
	repo := row.ID

	count := "all"
	if info.Tags > 0 {
		count = plural(info.Tags, "tag", "tags")
	} else if info.Tags == 0 {
		return m.flash(repo+" has no tags to delete", statusInfo, flashShort)
	}

	title := fmt.Sprintf("Delete every manifest in %s", repo)
	if m.cfg.DryRun {
		title = "DRY RUN — " + title
	}

	m.confirm = &confirmState{
		title: title,
		lines: []string{
			styleDanger.Render(fmt.Sprintf("This deletes %s from %s.", count, repo)),
			"",
			styleMutedT.Render("The repository disappears from the catalog once it holds no manifests."),
			styleMutedT.Render("Blob storage is reclaimed only by the registry's garbage collector."),
			"",
			styleLabel.Render("Type the repository name to confirm:"),
		},
		requireText: repo,
		run: func(mm *Model) tea.Cmd {
			return runRepoPurge(mm.deleteCtx(), mm.client, repo, mm.cfg.DryRun)
		},
	}
	m.mode = modeConfirm
	return nil
}

// deleteCtx returns the context deletes run under. Deletes are deliberately
// not tied to a view's stream: navigating away must not abort a delete that is
// already in flight.
func (m *Model) deleteCtx() context.Context { return context.Background() }
