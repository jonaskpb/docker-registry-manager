package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
	"github.com/jonaskpb/docker-registry-manager/internal/regtest"
)

// TestMain disables the UI's timers. Every test here drives the update loop
// by hand, and a real timer would either stall it or expire a status message
// the test is about to assert on.
func TestMain(m *testing.M) {
	tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	os.Exit(m.Run())
}

// driver runs the model's update loop synchronously so a test can drive the
// UI exactly as the Bubble Tea runtime would.
type driver struct {
	t     *testing.T
	m     *Model
	steps int
	quit  bool
}

func newDriver(t *testing.T, cfg Config, width, height int) *driver {
	t.Helper()
	d := &driver{t: t, m: New(cfg)}
	d.send(tea.WindowSizeMsg{Width: width, Height: height})
	d.run(d.m.Init())
	return d
}

// run executes a command and feeds the resulting message back in.
func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	d.steps++
	if d.steps > 5000 {
		d.t.Fatal("update loop did not settle")
	}
	msg := cmd()
	switch m := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, sub := range m {
			d.run(sub)
		}
	case tickMsg:
		// The spinner would otherwise drive the loop forever.
		return
	case tea.QuitMsg:
		d.quit = true
	default:
		d.send(msg)
	}
}

func (d *driver) send(msg tea.Msg) {
	_, cmd := d.m.Update(msg)
	d.run(cmd)
}

// press sends a keystroke.
func (d *driver) press(k string) {
	d.send(keyMsg(k))
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func (d *driver) typeText(s string) {
	for _, r := range s {
		d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// view renders the screen with styling stripped, for substring assertions.
func (d *driver) view() string { return stripANSI(d.m.View()) }

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' && s[i] != '\a' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// seed builds a registry with a shared-digest tag pair, a multi-platform
// index, and a second repository.
func seed(t *testing.T) (*regtest.Registry, *registry.Client) {
	t.Helper()
	reg := regtest.New()
	t.Cleanup(reg.Close)

	created := time.Now().Add(-96 * time.Hour)
	digest := reg.AddImage(regtest.Image{Repo: "app", Tag: "v1.0.0", Created: created, LayerSizes: []int64{4 << 20, 8 << 20}})
	reg.Tag("app", "latest", digest) // same manifest, second tag
	reg.AddImage(regtest.Image{Repo: "app", Tag: "v0.9.0", Created: created.Add(-24 * time.Hour), LayerSizes: []int64{3 << 20}})
	reg.AddIndex("app", "multi", []string{"linux/amd64", "linux/arm64"}, created)
	reg.AddImage(regtest.Image{Repo: "web", Tag: "edge", Created: created, LayerSizes: []int64{2 << 20}})

	client, err := registry.New(registry.Options{Address: reg.URL(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return reg, client
}

func TestBrowseRepositories(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client, Version: "test"}, 120, 30)

	view := d.view()
	if !strings.Contains(view, "Repositories(2)") {
		t.Errorf("repository count missing from the view:\n%s", view)
	}
	for _, want := range []string{"app", "web", "REPOSITORY", "TAGS"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}

	// The tag counts arrive from the background enrichment.
	row, _ := d.m.repos.Current()
	if got := rowRepo(row).Tags; got != 4 {
		t.Errorf("app has %d tags in the table, want 4", got)
	}
}

func TestDrillIntoTags(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press("enter") // open "app"

	if d.m.screen != screenTags {
		t.Fatalf("screen = %v, want tags", d.m.screen)
	}
	view := d.view()
	for _, want := range []string{"Images · app", "v1.0.0", "latest", "multi", "TAG", "DIGEST", "SIZE", "PLATFORMS"} {
		if !strings.Contains(view, want) {
			t.Errorf("tag view is missing %q:\n%s", want, view)
		}
	}

	// Enrichment fills in size and platform for a plain image...
	var v1 registry.TagInfo
	for _, r := range d.m.tags.Rows() {
		if r.ID == "v1.0.0" {
			v1 = rowTag(r)
		}
	}
	if v1.Size == 0 {
		t.Error("v1.0.0 has no size")
	}
	if strings.Join(v1.Platforms, ",") != "linux/amd64" {
		t.Errorf("v1.0.0 platforms = %v, want [linux/amd64]", v1.Platforms)
	}
	// ...and lists every platform of an index.
	for _, r := range d.m.tags.Rows() {
		if r.ID == "multi" {
			if got := strings.Join(rowTag(r).Platforms, ","); got != "linux/amd64,linux/arm64" {
				t.Errorf("multi platforms = %s", got)
			}
			if !rowTag(r).IsIndex {
				t.Error("multi not flagged as an index")
			}
		}
	}

	d.press("esc")
	if d.m.screen != screenRepos {
		t.Errorf("esc did not return to the repository list")
	}
}

func TestDescribeImage(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 40)

	d.press("enter") // app
	d.press("enter") // describe the first tag

	if d.m.screen != screenDetail {
		t.Fatalf("screen = %v, want detail", d.m.screen)
	}
	view := d.view()
	for _, want := range []string{"IMAGE", "Reference", "Digest", "Total size", "LAYERS", "MANIFEST"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail view is missing %q:\n%s", want, view)
		}
	}
}

// Deleting a tag that shares its manifest with another tag must say so before
// the user confirms: this is the surprising part of the registry delete API.
func TestDeleteWarnsAboutSharedDigest(t *testing.T) {
	reg, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press("enter") // app
	selectTag(t, d, "v1.0.0")
	d.press("ctrl+d")

	if d.m.mode != modeConfirm {
		t.Fatal("ctrl+d did not open a confirmation")
	}
	view := d.view()
	if !strings.Contains(view, "latest") {
		t.Errorf("confirmation does not mention the other tag on the same digest:\n%s", view)
	}
	if !strings.Contains(view, "will be removed too") {
		t.Errorf("confirmation does not warn about collateral tags:\n%s", view)
	}

	d.press("y")

	remaining := reg.TagsOf("app")
	if strings.Join(remaining, ",") != "multi,v0.9.0" {
		t.Errorf("tags after delete = %v, want [multi v0.9.0]", remaining)
	}
	if d.m.tags.Total() != 2 {
		t.Errorf("table shows %d tags after the delete, want 2", d.m.tags.Total())
	}
}

// Cancelling must send nothing to the registry.
func TestDeleteCancelled(t *testing.T) {
	reg, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press("enter")
	selectTag(t, d, "v0.9.0")
	d.press("ctrl+d")
	d.press("esc")

	if d.m.mode != modeNormal {
		t.Error("esc did not dismiss the confirmation")
	}
	if len(reg.TagsOf("app")) != 4 {
		t.Errorf("tags = %v, want all four still present", reg.TagsOf("app"))
	}
}

// Marking several tags and deleting them in one go.
func TestDeleteMarkedTags(t *testing.T) {
	reg, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press("enter")
	selectTag(t, d, "v0.9.0")
	d.press("space") // mark v0.9.0, cursor advances
	selectTag(t, d, "multi")
	d.press("space") // mark multi

	if d.m.tags.MarkCount() != 2 {
		t.Fatalf("mark count = %d, want 2", d.m.tags.MarkCount())
	}

	d.press("ctrl+d")
	d.press("enter") // confirm

	remaining := reg.TagsOf("app")
	if strings.Join(remaining, ",") != "latest,v1.0.0" {
		t.Errorf("tags after delete = %v, want [latest v1.0.0]", remaining)
	}
}

func TestReadOnlyRefusesDelete(t *testing.T) {
	reg, client := seed(t)
	d := newDriver(t, Config{Client: client, ReadOnly: true}, 120, 30)

	d.press("enter")
	d.press("ctrl+d")

	if d.m.mode == modeConfirm {
		t.Fatal("read-only mode opened a delete confirmation")
	}
	if !strings.Contains(d.view(), "read-only") {
		t.Errorf("no explanation shown in read-only mode:\n%s", d.view())
	}
	if len(reg.TagsOf("app")) != 4 {
		t.Error("tags changed in read-only mode")
	}
}

func TestDryRunSendsNoDelete(t *testing.T) {
	reg, client := seed(t)
	d := newDriver(t, Config{Client: client, DryRun: true}, 120, 30)

	d.press("enter")
	selectTag(t, d, "v0.9.0")
	d.press("ctrl+d")

	if !strings.Contains(d.view(), "DRY RUN") {
		t.Errorf("confirmation is not labelled as a dry run:\n%s", d.view())
	}
	d.press("y")

	if len(reg.TagsOf("app")) != 4 {
		t.Errorf("a dry run deleted tags: %v", reg.TagsOf("app"))
	}
	if !strings.Contains(d.view(), "would delete") {
		t.Errorf("dry run did not report what it would have done:\n%s", d.view())
	}
}

// A whole-repository purge is gated behind typing the repository name.
func TestPurgeRepositoryRequiresTypedName(t *testing.T) {
	reg, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	selectRepo(t, d, "web")
	d.press("ctrl+d")

	if d.m.mode != modeConfirm {
		t.Fatal("ctrl+d on a repository did not open a confirmation")
	}
	if d.m.confirm.requireText != "web" {
		t.Errorf("requireText = %q, want %q", d.m.confirm.requireText, "web")
	}

	// Enter with nothing typed must not delete anything.
	d.press("enter")
	if len(reg.TagsOf("web")) != 1 {
		t.Fatal("repository purged without the confirmation phrase")
	}
	if d.m.mode != modeConfirm {
		t.Fatal("confirmation closed even though the phrase was wrong")
	}

	d.typeText("web")
	d.press("enter")

	if got := reg.TagsOf("web"); len(got) != 0 {
		t.Errorf("tags after purge = %v, want none", got)
	}
}

// The delete triggers a reload of the tag list, and that reload must not
// overwrite the report of what was deleted before the user can read it.
func TestDeleteReportSurvivesTheRefresh(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press("enter")
	selectTag(t, d, "v0.9.0")
	d.press("ctrl+d")
	d.press("y")

	if !strings.Contains(d.view(), "deleted 1 manifest") {
		t.Errorf("the delete report was overwritten by the refresh:\n%s", d.view())
	}
	// The table behind it did reload.
	if d.m.tags.Total() != 3 {
		t.Errorf("table shows %d tags, want 3", d.m.tags.Total())
	}
}

// The purge dialog's text field has to accept every character of a repository
// name, including the y and n that confirm and cancel a simple dialog.
func TestPurgeConfirmationAcceptsYAndN(t *testing.T) {
	reg := regtest.New()
	t.Cleanup(reg.Close)
	reg.AddImage(regtest.Image{Repo: "infra/nginx", Tag: "1.27", LayerSizes: []int64{1 << 20}})
	client, err := registry.New(registry.Options{Address: reg.URL(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	d := newDriver(t, Config{Client: client}, 120, 30)
	selectRepo(t, d, "infra/nginx")
	d.press("ctrl+d")

	d.typeText("infra/nginx")
	if got := d.m.confirm.input.Value(); got != "infra/nginx" {
		t.Fatalf("typed phrase = %q, want %q (y/n must not be swallowed)", got, "infra/nginx")
	}
	d.press("enter")
	if got := reg.TagsOf("infra/nginx"); len(got) != 0 {
		t.Errorf("tags after purge = %v, want none", got)
	}
}

func TestFilter(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press("enter") // app
	d.press("/")
	d.typeText("v1")

	if d.m.tags.Len() != 1 {
		t.Errorf("filter v1 matched %d tags, want 1", d.m.tags.Len())
	}
	if !strings.Contains(d.view(), "/v1") {
		t.Errorf("active filter not shown:\n%s", d.view())
	}

	d.press("esc") // leave filter mode, clearing it
	if d.m.tags.Len() != 4 {
		t.Errorf("esc did not clear the filter: %d rows", d.m.tags.Len())
	}
}

func TestCommandModeJumpsToRepository(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client}, 120, 30)

	d.press(":")
	d.typeText("web")
	d.press("enter")

	if d.m.screen != screenTags || d.m.repo != "web" {
		t.Errorf("':web' did not open the web repository (screen=%v repo=%q)", d.m.screen, d.m.repo)
	}

	d.press(":")
	d.typeText("repos")
	d.press("enter")
	if d.m.screen != screenRepos {
		t.Error("':repos' did not return to the repository list")
	}

	d.press(":")
	d.typeText("quit")
	d.press("enter")
	if !d.quit {
		t.Error("':quit' did not quit")
	}
}

func TestHelpScreen(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client, Version: "v1.2.3"}, 120, 40)

	d.press("?")
	view := d.view()
	for _, want := range []string{"Navigation", "Deleting images", "v1.2.3"} {
		if !strings.Contains(view, want) {
			t.Errorf("help is missing %q:\n%s", want, view)
		}
	}

	// The help text is taller than the terminal, so it has to scroll.
	if !strings.Contains(view, "(1-") {
		t.Errorf("help does not show a scroll position:\n%s", view)
	}
	d.press("G")
	if bottom := d.view(); !strings.Contains(bottom, "garbage collector") {
		t.Errorf("scrolling to the end did not reveal the deletion notes:\n%s", bottom)
	}
	d.press("g")
	if top := d.view(); !strings.Contains(top, "Navigation") {
		t.Errorf("scrolling back to the top failed:\n%s", top)
	}

	d.press("esc")
	if d.m.screen != screenRepos {
		t.Error("esc did not leave the help screen")
	}
}

// The rendered frame must fit the terminal exactly at any size, or it scrolls
// and smears.
func TestRenderFitsTerminal(t *testing.T) {
	_, client := seed(t)

	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}, {200, 50}, {60, 20}} {
		width, height := size[0], size[1]
		d := newDriver(t, Config{Client: client}, width, height)

		screens := []func(){
			func() {},                               // repositories
			func() { d.press("enter") },             // tags
			func() { d.press("enter") },             // detail
			func() { d.press("esc"); d.press("?") }, // help
		}
		for i, setup := range screens {
			setup()
			out := d.m.View()
			lines := strings.Split(out, "\n")
			if len(lines) != height {
				t.Errorf("%dx%d screen %d: rendered %d lines, want %d", width, height, i, len(lines), height)
			}
			for n, line := range lines {
				if w := visibleWidth(line); w > width {
					t.Errorf("%dx%d screen %d: line %d is %d cells wide", width, height, i, n, w)
				}
			}
		}
	}
}

// The confirmation dialog is drawn over the body and must not overflow either.
func TestConfirmDialogFitsTerminal(t *testing.T) {
	_, client := seed(t)
	d := newDriver(t, Config{Client: client}, 100, 30)

	d.press("enter")
	d.press("ctrl+d")

	lines := strings.Split(d.m.View(), "\n")
	if len(lines) != 30 {
		t.Errorf("dialog frame is %d lines, want 30", len(lines))
	}
	for n, line := range lines {
		if w := visibleWidth(line); w > 100 {
			t.Errorf("dialog line %d is %d cells wide", n, w)
		}
	}
}

func selectTag(t *testing.T, d *driver, tag string) {
	t.Helper()
	for i := 0; i < d.m.tags.Len(); i++ {
		d.m.tags.MoveTo(i)
		if row, ok := d.m.tags.Current(); ok && row.ID == tag {
			return
		}
	}
	t.Fatalf("tag %q not in the table", tag)
}

func selectRepo(t *testing.T, d *driver, repo string) {
	t.Helper()
	for i := 0; i < d.m.repos.Len(); i++ {
		d.m.repos.MoveTo(i)
		if row, ok := d.m.repos.Current(); ok && row.ID == repo {
			return
		}
	}
	t.Fatalf("repository %q not in the table", repo)
}
