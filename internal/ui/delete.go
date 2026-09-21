package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
)

// deleteTarget is one manifest scheduled for deletion. A registry delete
// always addresses a digest, so Tags lists every tag that resolves to it --
// all of them disappear together, and the confirmation says so.
type deleteTarget struct {
	Repo   string
	Digest string
	Tags   []string
}

// Label renders the target for the confirmation dialog.
func (t deleteTarget) Label() string {
	tags := "(untagged)"
	if len(t.Tags) > 0 {
		tags = strings.Join(t.Tags, ", ")
	}
	return fmt.Sprintf("%s:%s", t.Repo, tags)
}

type deleteResult struct {
	Target deleteTarget
	Err    error
}

// planDeletes collapses the selected tags into one target per distinct
// manifest digest, folding in any other tag in the same repository that shares
// that digest. all is the full row set of the current repository, which is
// what makes the "this also removes these tags" warning possible.
func planDeletes(selected []registry.TagInfo, all []registry.TagInfo) []deleteTarget {
	// digest -> every tag in the repo pointing at it
	shared := map[string][]string{}
	for _, t := range all {
		if t.Digest == "" {
			continue
		}
		shared[t.Repo+"|"+t.Digest] = append(shared[t.Repo+"|"+t.Digest], t.Tag)
	}

	var (
		targets []deleteTarget
		seen    = map[string]bool{}
	)
	for _, t := range selected {
		key := t.Repo + "|" + t.Digest
		if t.Digest == "" {
			// Not enriched yet (or the manifest lookup failed): fall back to
			// resolving the digest at delete time, keyed by tag so the same
			// tag is not queued twice.
			key = t.Repo + "|tag:" + t.Tag
		}
		if seen[key] {
			continue
		}
		seen[key] = true

		tags := shared[t.Repo+"|"+t.Digest]
		if t.Digest == "" || len(tags) == 0 {
			tags = []string{t.Tag}
		}
		sort.Strings(tags)
		targets = append(targets, deleteTarget{Repo: t.Repo, Digest: t.Digest, Tags: tags})
	}
	return targets
}

// deleteConcurrency bounds parallel deletes. Deleting is cheap server-side but
// a whole-repository purge should still not open a hundred sockets at once.
const deleteConcurrency = 4

// runDeletes deletes every target and reports the outcome to the UI.
func runDeletes(ctx context.Context, c *registry.Client, targets []deleteTarget, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		return deleteDoneMsg{results: executeDeletes(ctx, c, targets, dryRun)}
	}
}

// executeDeletes deletes every target, resolving any digest that is still
// unknown first. It never stops at the first failure: each target reports its
// own outcome so a partial success is visible rather than silently lost.
func executeDeletes(ctx context.Context, c *registry.Client, targets []deleteTarget, dryRun bool) []deleteResult {
	results := make([]deleteResult, len(targets))
	sem := make(chan struct{}, deleteConcurrency)
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, target deleteTarget) {
			defer wg.Done()
			defer func() { <-sem }()

			if target.Digest == "" {
				if len(target.Tags) == 0 {
					results[i] = deleteResult{Target: target, Err: fmt.Errorf("no digest and no tag to resolve it from")}
					return
				}
				resolved, err := c.ManifestDigest(ctx, target.Repo, target.Tags[0])
				if err != nil {
					results[i] = deleteResult{Target: target, Err: err}
					return
				}
				target.Digest = resolved
			}
			if dryRun {
				results[i] = deleteResult{Target: target}
				return
			}
			results[i] = deleteResult{Target: target, Err: c.DeleteManifest(ctx, target.Repo, target.Digest)}
		}(i, target)
	}
	wg.Wait()
	return results
}

// summarizeDeletes turns delete results into a status line and, when something
// failed, the detail worth showing.
func summarizeDeletes(results []deleteResult, dryRun bool) (msg string, level statusLevel) {
	var ok int
	var failures []string
	for _, r := range results {
		if r.Err == nil {
			ok++
			continue
		}
		failures = append(failures, fmt.Sprintf("%s: %v", r.Target.Label(), r.Err))
	}

	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}

	switch {
	case len(failures) == 0:
		return fmt.Sprintf("%s %s", verb, plural(ok, "manifest", "manifests")), statusOK
	case ok == 0:
		return "delete failed — " + failures[0], statusError
	default:
		return fmt.Sprintf("%s %d of %d — %s", verb, ok, len(results), failures[0]), statusWarn
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// runRepoPurge deletes every manifest in a repository: it lists the tags,
// resolves each to its digest, collapses tags that share one, and deletes each
// distinct manifest once.
func runRepoPurge(ctx context.Context, c *registry.Client, repo string, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		tags, err := c.Tags(ctx, repo)
		if err != nil {
			return deleteDoneMsg{results: []deleteResult{{
				Target: deleteTarget{Repo: repo},
				Err:    fmt.Errorf("listing tags of %s: %w", repo, err),
			}}}
		}
		if len(tags) == 0 {
			return deleteDoneMsg{}
		}

		digests, failures := c.ResolveDigests(ctx, repo, tags)

		byDigest := map[string][]string{}
		for tag, digest := range digests {
			byDigest[digest] = append(byDigest[digest], tag)
		}

		targets := make([]deleteTarget, 0, len(byDigest))
		for digest, tagList := range byDigest {
			sort.Strings(tagList)
			targets = append(targets, deleteTarget{Repo: repo, Digest: digest, Tags: tagList})
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].Tags[0] < targets[j].Tags[0] })

		results := executeDeletes(ctx, c, targets, dryRun)
		// Surface tags we could not even resolve, so a partial purge is never
		// reported as a complete one.
		for tag, err := range failures {
			results = append(results, deleteResult{
				Target: deleteTarget{Repo: repo, Tags: []string{tag}},
				Err:    err,
			})
		}
		return deleteDoneMsg{results: results}
	}
}
