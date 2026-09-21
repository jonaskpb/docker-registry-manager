package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jonaskpb/docker-registry-manager/internal/regtest"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		in      string
		repo    string
		ref     string
		wantErr bool
	}{
		{in: "app:v1", repo: "app", ref: "v1"},
		{in: "team/app:v1.2.3", repo: "team/app", ref: "v1.2.3"},
		{in: "app", repo: "app", ref: "latest"},
		{in: "a/b/c:tag", repo: "a/b/c", ref: "tag"},
		{in: "app@sha256:abc123", repo: "app", ref: "sha256:abc123"},
		{in: "", wantErr: true},
		{in: "@sha256:abc", wantErr: true},
	}
	for _, tc := range cases {
		repo, ref, err := parseRef(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseRef(%q) should have failed", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRef(%q): %v", tc.in, err)
			continue
		}
		if repo != tc.repo || ref != tc.ref {
			t.Errorf("parseRef(%q) = (%q, %q), want (%q, %q)", tc.in, repo, ref, tc.repo, tc.ref)
		}
	}
}

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	code := fn()
	w.Close()
	os.Stdout = old
	return <-done, code
}

// seedRegistry builds a registry with a shared-digest tag pair.
func seedRegistry(t *testing.T) *regtest.Registry {
	t.Helper()
	reg := regtest.New()
	t.Cleanup(reg.Close)

	digest := reg.AddImage(regtest.Image{Repo: "app", Tag: "v1", LayerSizes: []int64{4 << 20}})
	reg.Tag("app", "latest", digest)
	reg.AddImage(regtest.Image{Repo: "app", Tag: "v2", LayerSizes: []int64{6 << 20}})
	reg.AddImage(regtest.Image{Repo: "web", Tag: "edge", LayerSizes: []int64{1 << 20}})
	return reg
}

func TestListCommand(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"ls", "--registry", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}
	if got := strings.Fields(out); len(got) != 2 || got[0] != "app" || got[1] != "web" {
		t.Errorf("ls printed %q, want app and web", out)
	}
}

func TestListJSON(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"ls", "--json", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(out, `"repositories"`) || !strings.Contains(out, `"app"`) {
		t.Errorf("unexpected JSON:\n%s", out)
	}
}

func TestTagsCommand(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"tags", "app", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}
	for _, want := range []string{"TAG", "DIGEST", "SIZE", "v1", "v2", "latest", "linux/amd64"} {
		if !strings.Contains(out, want) {
			t.Errorf("tags output is missing %q:\n%s", want, out)
		}
	}
}

// Flags must work before or after the sub-command and its arguments.
func TestFlagsInterleaveWithArguments(t *testing.T) {
	reg := seedRegistry(t)
	for _, args := range [][]string{
		{"tags", "app", "-r", reg.URL()},
		{"-r", reg.URL(), "tags", "app"},
		{"tags", "-r", reg.URL(), "app"},
	} {
		out, code := capture(t, func() int { return Run(args, "test") })
		if code != 0 || !strings.Contains(out, "v1") {
			t.Errorf("args %v: exit %d, output:\n%s", args, code, out)
		}
	}
}

// Without --yes, rm must print its plan and delete nothing (stdin is not a
// terminal here, which stands in for the user declining).
func TestRemoveWithoutConfirmationDeletesNothing(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"rm", "app:v2", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}
	if !strings.Contains(out, "cancelled") {
		t.Errorf("expected the deletion to be cancelled:\n%s", out)
	}
	if len(reg.TagsOf("app")) != 3 {
		t.Errorf("tags = %v, want all three still present", reg.TagsOf("app"))
	}
}

func TestRemoveWithYes(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"rm", "app:v2", "--yes", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}
	if got := strings.Join(reg.TagsOf("app"), ","); got != "latest,v1" {
		t.Errorf("tags after rm = %q, want \"latest,v1\"", got)
	}
	if !strings.Contains(out, "garbage collector") {
		t.Errorf("rm did not mention that blobs still need collecting:\n%s", out)
	}
}

// Deleting one of two tags on the same manifest removes both; the plan has to
// say so before the user agrees.
func TestRemoveWarnsAboutCollateralTags(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"rm", "app:v1", "--dry-run", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(out, "also untags latest") {
		t.Errorf("plan does not warn that 'latest' goes too:\n%s", out)
	}
	if len(reg.TagsOf("app")) != 3 {
		t.Errorf("--dry-run deleted something: %v", reg.TagsOf("app"))
	}
}

func TestRemoveAll(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"rm", "--all", "app", "--yes", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}
	if got := reg.TagsOf("app"); len(got) != 0 {
		t.Errorf("tags after purge = %v, want none", got)
	}
	// Two distinct manifests, even though three tags pointed at them.
	if !strings.Contains(out, "2 manifests") {
		t.Errorf("expected two manifests in the plan:\n%s", out)
	}
	if len(reg.TagsOf("web")) != 1 {
		t.Error("purging app touched web")
	}
}

func TestReadOnlyBlocksRemove(t *testing.T) {
	reg := seedRegistry(t)
	_, code := capture(t, func() int {
		return Run([]string{"rm", "app:v2", "--yes", "--read-only", "-r", reg.URL()}, "test")
	})
	if code == 0 {
		t.Error("rm should fail under --read-only")
	}
	if len(reg.TagsOf("app")) != 3 {
		t.Errorf("--read-only did not prevent the delete: %v", reg.TagsOf("app"))
	}
}

func TestInspect(t *testing.T) {
	reg := seedRegistry(t)
	out, code := capture(t, func() int {
		return Run([]string{"inspect", "app:v1", "-r", reg.URL()}, "test")
	})
	if code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out)
	}
	for _, want := range []string{"Repository:", "Digest:", "sha256:", "Layers:", "schemaVersion"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output is missing %q:\n%s", want, out)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	out, code := capture(t, func() int { return Run([]string{"--version"}, "v9.9.9") })
	if code != 0 || !strings.Contains(out, "v9.9.9") {
		t.Errorf("--version printed %q (exit %d)", out, code)
	}

	out, code = capture(t, func() int { return Run([]string{"--help"}, "test") })
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("--help printed %q (exit %d)", out, code)
	}
}

func TestUnknownFlagIsAnError(t *testing.T) {
	_, code := capture(t, func() int { return Run([]string{"--nope"}, "test") })
	if code != 2 {
		t.Errorf("exit code for an unknown flag = %d, want 2", code)
	}
}
