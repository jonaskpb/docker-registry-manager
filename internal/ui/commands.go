package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
)

// Every message carries the generation of the load that produced it. A view
// change or a refresh bumps the generation, so results from a superseded load
// are recognised and dropped instead of repainting a table the user has
// already navigated away from.

type reposLoadedMsg struct {
	gen   int
	repos []string
	err   error
}

type repoInfoMsg struct {
	gen  int
	info registry.RepoInfo
}

type tagsLoadedMsg struct {
	gen  int
	repo string
	tags []string
	err  error
}

type tagInfoMsg struct {
	gen  int
	info registry.TagInfo
}

// streamKind names which background enrichment finished.
type streamKind int

const (
	streamRepos streamKind = iota
	streamTags
)

type streamDoneMsg struct {
	gen  int
	kind streamKind
}

type detailMsg struct {
	gen     int
	title   string
	content string
	err     error
}

type deleteDoneMsg struct {
	results []deleteResult
}

type flashExpiredMsg struct{ token int }

type tickMsg time.Time

// loadRepos fetches the catalog.
func loadRepos(ctx context.Context, c *registry.Client, gen int) tea.Cmd {
	return func() tea.Msg {
		repos, err := c.Catalog(ctx)
		if err != nil {
			return reposLoadedMsg{gen: gen, err: err}
		}
		sort.Strings(repos)
		return reposLoadedMsg{gen: gen, repos: repos}
	}
}

func pumpRepoInfo(ch <-chan registry.RepoInfo, gen int) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		info, ok := <-ch
		if !ok {
			return streamDoneMsg{gen: gen, kind: streamRepos}
		}
		return repoInfoMsg{gen: gen, info: info}
	}
}

// loadTags fetches the tag list of a repository.
func loadTags(ctx context.Context, c *registry.Client, gen int, repo string) tea.Cmd {
	return func() tea.Msg {
		tags, err := c.Tags(ctx, repo)
		if err != nil {
			return tagsLoadedMsg{gen: gen, repo: repo, err: err}
		}
		sort.Strings(tags)
		return tagsLoadedMsg{gen: gen, repo: repo, tags: tags}
	}
}

func pumpTagInfo(ch <-chan registry.TagInfo, gen int) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		info, ok := <-ch
		if !ok {
			return streamDoneMsg{gen: gen, kind: streamTags}
		}
		return tagInfoMsg{gen: gen, info: info}
	}
}

// loadDetail builds the manifest detail page for a tag: a human-readable
// summary followed by the manifest and image config as served.
func loadDetail(ctx context.Context, c *registry.Client, gen int, repo, tag string) tea.Cmd {
	return func() tea.Msg {
		m, err := c.Manifest(ctx, repo, tag)
		if err != nil {
			return detailMsg{gen: gen, err: err}
		}
		title := fmt.Sprintf("%s:%s", repo, tag)

		var cfg *registry.ImageConfig
		if !m.IsIndex() {
			cfg, _ = c.ImageConfig(ctx, repo, m.Config)
		}
		return detailMsg{gen: gen, title: title, content: renderDetail(c.Host(), repo, tag, m, cfg)}
	}
}

// prettyJSON re-indents raw JSON for display, falling back to the original
// bytes if the registry served something that does not parse.
func prettyJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// tick schedules a message to arrive after d. It is a variable so tests can
// drive the update loop to a standstill instead of waiting out real timers.
var tick = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	return tea.Tick(d, fn)
}

// flashFor clears a transient status message after a delay.
func flashFor(token int, d time.Duration) tea.Cmd {
	return tick(d, func(time.Time) tea.Msg { return flashExpiredMsg{token: token} })
}

// spinnerTick drives the loading indicator.
func spinnerTick() tea.Cmd {
	return tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}
