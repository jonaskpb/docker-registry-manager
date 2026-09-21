// Package ui implements the terminal interface: a k9s-style browser over a
// container registry's repositories, tags and manifests.
package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
)

type screen int

const (
	screenRepos screen = iota
	screenTags
	screenDetail
	screenHelp
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeFilter
	modeCommand
	modeConfirm
)

type statusLevel int

const (
	statusInfo statusLevel = iota
	statusOK
	statusWarn
	statusError
)

// Config wires the UI to a registry and records the safety switches the user
// started with.
type Config struct {
	Client   *registry.Client
	Username string
	Version  string
	ReadOnly bool
	DryRun   bool
}

// confirmState is a destructive action waiting for the user to agree to it.
type confirmState struct {
	title string
	lines []string
	// requireText, when set, must be typed verbatim before the action can
	// run. Whole-repository purges use it; single deletes do not.
	requireText string
	input       input
	run         func(*Model) tea.Cmd
}

// stream identifies one of the independent background loads. Each has its own
// generation counter and cancel function, so drilling into a repository does
// not abandon the repository list's tag-count enrichment, and opening a detail
// page does not abandon the tag enrichment behind it.
type stream struct {
	gen     int
	ctx     context.Context
	cancel  context.CancelFunc
	loading bool
}

// next cancels any in-flight work on this stream and opens a new generation.
func (s *stream) next() (context.Context, int) {
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.gen++
	s.loading = true
	return s.ctx, s.gen
}

// Model is the root Bubble Tea model.
type Model struct {
	cfg    Config
	client *registry.Client

	screen screen
	mode   inputMode

	repos *Table
	tags  *Table

	repo string // repository shown by the tags screen

	detailTitle  string
	detailRef    string
	detailLines  []string
	detailOffset int

	helpLines  []string
	helpOffset int

	width, height int

	repoStream   stream
	tagStream    stream
	detailStream stream

	repoCh <-chan registry.RepoInfo
	tagCh  <-chan registry.TagInfo

	frame int

	status string
	level  statusLevel
	// A flashed message stays put until stickyUntil passes, so the refresh a
	// delete triggers cannot wipe the report of what the delete did.
	flashToken  int
	stickyUntil time.Time
	fatal       string

	filterInput input
	cmdInput    input
	confirm     *confirmState
}

// New builds the root model.
func New(cfg Config) *Model {
	return &Model{
		cfg:         cfg,
		client:      cfg.Client,
		screen:      screenRepos,
		repos:       NewTable(repoColumns()),
		tags:        NewTable(tagColumns()),
		filterInput: input{Placeholder: "filter — regex or substring"},
		cmdInput:    input{Placeholder: "repos | help | quit | <repository>"},
		status:      "connecting to " + cfg.Client.Host() + "…",
	}
}

func repoColumns() []Column {
	return []Column{
		{Title: "REPOSITORY", MinWidth: 16},
		{Title: "TAGS", Width: 6, Right: true, Less: func(a, b Row) bool {
			return rowRepo(a).Tags < rowRepo(b).Tags
		}},
	}
}

func tagColumns() []Column {
	return []Column{
		{Title: "TAG", MinWidth: 12},
		{Title: "DIGEST", Width: 12},
		{Title: "SIZE", Width: 10, Right: true, Less: func(a, b Row) bool {
			return rowTag(a).Size < rowTag(b).Size
		}},
		{Title: "AGE", Width: 5, Right: true, Less: func(a, b Row) bool {
			return rowTag(a).Created.Before(rowTag(b).Created)
		}},
		{Title: "PLATFORMS", Width: 24, MinWidth: 9},
		{Title: "LAYERS", Width: 6, Right: true, Less: func(a, b Row) bool {
			return rowTag(a).Layers < rowTag(b).Layers
		}},
	}
}

func rowRepo(r Row) registry.RepoInfo {
	info, _ := r.Data.(registry.RepoInfo)
	return info
}

func rowTag(r Row) registry.TagInfo {
	info, _ := r.Data.(registry.TagInfo)
	return info
}

// Init kicks off the first catalog load.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.reloadRepos(), tea.SetWindowTitle("drm — "+m.client.Host()))
}

// busy reports whether any background load is running.
func (m *Model) busy() bool {
	return m.repoStream.loading || m.tagStream.loading || m.detailStream.loading
}

func (m *Model) reloadRepos() tea.Cmd {
	ctx, gen := m.repoStream.next()
	m.repoCh = nil
	m.fatal = ""
	return tea.Batch(loadRepos(ctx, m.client, gen), spinnerTick())
}

func (m *Model) openRepo(repo string) tea.Cmd {
	ctx, gen := m.tagStream.next()
	m.tagCh = nil
	m.repo = repo
	m.screen = screenTags
	m.fatal = ""
	m.tags.ClearMarks()
	m.tags.SetRows(nil)
	m.tags.SetFilter("")
	return tea.Batch(loadTags(ctx, m.client, gen, repo), spinnerTick())
}

func (m *Model) openDetail(repo, tag string) tea.Cmd {
	ctx, gen := m.detailStream.next()
	m.detailTitle = repo + ":" + tag
	m.detailRef = m.client.Host() + "/" + repo + ":" + tag
	m.detailLines = nil
	m.detailOffset = 0
	m.screen = screenDetail
	return tea.Batch(loadDetail(ctx, m.client, gen, repo, tag), spinnerTick())
}

// Update handles every message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		m.frame++
		if m.busy() {
			return m, spinnerTick()
		}
		return m, nil

	case reposLoadedMsg:
		if msg.gen != m.repoStream.gen {
			return m, nil
		}
		if msg.err != nil {
			m.repoStream.loading = false
			m.fatal = msg.err.Error()
			return m, m.flash("catalog failed — "+msg.err.Error(), statusError, flashError)
		}
		rows := make([]Row, 0, len(msg.repos))
		for _, name := range msg.repos {
			rows = append(rows, repoRow(registry.RepoInfo{Name: name, Tags: -1}))
		}
		m.repos.SetRows(rows)
		if m.screen == screenRepos {
			m.setStatus(fmt.Sprintf("%s in %s", plural(len(msg.repos), "repository", "repositories"), m.client.Host()), statusOK)
		}
		if len(msg.repos) == 0 {
			m.repoStream.loading = false
			return m, nil
		}
		m.repoCh = m.client.DescribeRepos(m.repoStream.ctx, msg.repos)
		return m, pumpRepoInfo(m.repoCh, msg.gen)

	case repoInfoMsg:
		if msg.gen != m.repoStream.gen {
			return m, nil
		}
		m.repos.UpsertRow(repoRow(msg.info))
		return m, pumpRepoInfo(m.repoCh, msg.gen)

	case tagsLoadedMsg:
		if msg.gen != m.tagStream.gen {
			return m, nil
		}
		if msg.err != nil {
			m.tagStream.loading = false
			m.fatal = msg.err.Error()
			return m, m.flash(fmt.Sprintf("%s — %v", msg.repo, msg.err), statusError, flashError)
		}
		rows := make([]Row, 0, len(msg.tags))
		for _, tag := range msg.tags {
			rows = append(rows, tagRow(registry.TagInfo{Repo: msg.repo, Tag: tag}))
		}
		m.tags.SetRows(rows)
		m.setStatus(fmt.Sprintf("%s — %s", msg.repo, plural(len(msg.tags), "tag", "tags")), statusOK)
		if len(msg.tags) == 0 {
			m.tagStream.loading = false
			return m, nil
		}
		m.tagCh = m.client.DescribeTags(m.tagStream.ctx, msg.repo, msg.tags)
		return m, pumpTagInfo(m.tagCh, msg.gen)

	case tagInfoMsg:
		if msg.gen != m.tagStream.gen {
			return m, nil
		}
		m.tags.UpsertRow(tagRow(msg.info))
		return m, pumpTagInfo(m.tagCh, msg.gen)

	case streamDoneMsg:
		switch msg.kind {
		case streamRepos:
			if msg.gen == m.repoStream.gen {
				m.repoStream.loading = false
			}
		case streamTags:
			if msg.gen == m.tagStream.gen {
				m.tagStream.loading = false
			}
		}
		return m, nil

	case detailMsg:
		if msg.gen != m.detailStream.gen {
			return m, nil
		}
		m.detailStream.loading = false
		if msg.err != nil {
			m.screen = screenTags
			return m, m.flash("describe failed — "+msg.err.Error(), statusError, flashError)
		}
		m.detailTitle = msg.title
		m.detailLines = strings.Split(strings.TrimRight(msg.content, "\n"), "\n")
		m.detailOffset = 0
		return m, nil

	case deleteDoneMsg:
		text, level := summarizeDeletes(msg.results, m.cfg.DryRun)
		hold := flashLong
		if level == statusError {
			hold = flashError
		}
		// The reload below repaints the table and would otherwise overwrite
		// this line before the user has read it.
		report := m.flash(text, level, hold)
		if m.cfg.DryRun {
			return m, report
		}
		// Re-read the affected listing so the table reflects the registry.
		if m.screen == screenTags {
			return m, tea.Batch(report, m.openRepo(m.repo), m.reloadRepos())
		}
		return m, tea.Batch(report, m.reloadRepos())

	case statusMsg:
		return m, m.flash(msg.text, msg.level, flashShort)

	case flashExpiredMsg:
		if msg.token == m.flashToken {
			m.stickyUntil = time.Time{}
			m.status, m.level = m.defaultStatus(), statusInfo
		}
		return m, nil
	}
	return m, nil
}

// How long a flashed message holds the status line.
const (
	flashShort = 3 * time.Second
	flashLong  = 8 * time.Second
	flashError = 20 * time.Second
)

// setStatus shows a routine message -- a count, a name, the current view. It
// yields to a flashed message that has not expired yet.
func (m *Model) setStatus(text string, level statusLevel) {
	if time.Now().Before(m.stickyUntil) {
		return
	}
	m.status, m.level = text, level
}

// flash shows a message that must be read before it is replaced, and returns
// the command that restores the routine status afterwards.
func (m *Model) flash(text string, level statusLevel, d time.Duration) tea.Cmd {
	m.status, m.level = text, level
	m.flashToken++
	m.stickyUntil = time.Now().Add(d)
	return flashFor(m.flashToken, d)
}

// defaultStatus is what the status line falls back to when nothing has
// happened recently.
func (m *Model) defaultStatus() string {
	switch m.screen {
	case screenTags:
		return fmt.Sprintf("%s — %s", m.repo, plural(m.tags.Total(), "tag", "tags"))
	case screenDetail:
		return m.detailTitle
	case screenHelp:
		return "help — press esc to go back"
	default:
		return fmt.Sprintf("%s in %s", plural(m.repos.Total(), "repository", "repositories"), m.client.Host())
	}
}

// currentTable is the table the key handler operates on, or nil on screens
// that have none.
func (m *Model) currentTable() *Table {
	switch m.screen {
	case screenRepos:
		return m.repos
	case screenTags:
		return m.tags
	}
	return nil
}

func repoRow(info registry.RepoInfo) Row {
	tags := "…"
	switch {
	case info.Err != nil:
		tags = "err"
	case info.Tags >= 0:
		tags = itoa(info.Tags)
	}
	return Row{
		ID:     info.Name,
		Cells:  []string{info.Name, tags},
		Faint:  info.Tags == 0 && info.Err == nil,
		Danger: info.Err != nil,
		Data:   info,
	}
}

func tagRow(info registry.TagInfo) Row {
	digest, size, age, platforms, layers := "…", "…", "…", "…", "…"
	switch {
	case info.Err != nil:
		digest, size, age, layers = "error", "-", "-", "-"
		platforms = truncate(info.Err.Error(), 40)
	case info.Digest != "":
		digest = registry.ShortDigest(info.Digest)
		size = humanSize(info.Size)
		age = humanAge(info.Created)
		platforms = joinLimit(info.Platforms, 24)
		layers = itoa(info.Layers)
		if info.IsIndex {
			layers = "index"
		}
	}
	return Row{
		ID:     info.Tag,
		Cells:  []string{info.Tag, digest, size, age, platforms, layers},
		Danger: info.Err != nil,
		Data:   info,
	}
}

// statusMsg lets a command push a status line update.
type statusMsg struct {
	text  string
	level statusLevel
}

// yank copies text to the system clipboard using the OSC 52 escape sequence,
// which reaches the user's clipboard through SSH and tmux where a local
// clipboard tool would not.
func yank(text, label string) tea.Cmd {
	return func() tea.Msg {
		fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
		return statusMsg{text: "copied " + label, level: statusOK}
	}
}

// selectedTags returns the tag info behind the current selection.
func (m *Model) selectedTags() []registry.TagInfo {
	rows := m.tags.Selection()
	out := make([]registry.TagInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowTag(r))
	}
	return out
}

// allTags returns every tag row of the current repository, used to work out
// which tags share a digest with the ones being deleted.
func (m *Model) allTags() []registry.TagInfo {
	rows := m.tags.Rows()
	out := make([]registry.TagInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowTag(r))
	}
	return out
}
