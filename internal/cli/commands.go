package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
)

// signalContext returns a context cancelled on SIGINT/SIGTERM so a long
// listing can be interrupted cleanly.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runList prints the repositories in the catalog.
func runList(ctx context.Context, opts *options) error {
	client, _, err := newClient(opts)
	if err != nil {
		return err
	}
	repos, err := client.Catalog(ctx)
	if err != nil {
		return err
	}
	sort.Strings(repos)

	if opts.jsonOut {
		return writeJSON(map[string]any{"registry": client.Address(), "repositories": repos})
	}
	for _, r := range repos {
		fmt.Println(r)
	}
	return nil
}

// runTags prints the tags of a repository with their digest, size and age.
func runTags(ctx context.Context, opts *options, repo string) error {
	client, _, err := newClient(opts)
	if err != nil {
		return err
	}
	tags, err := client.Tags(ctx, repo)
	if err != nil {
		return err
	}
	sort.Strings(tags)

	infos := make([]registry.TagInfo, 0, len(tags))
	for info := range client.DescribeTags(ctx, repo, tags) {
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Tag < infos[j].Tag })

	if opts.jsonOut {
		out := make([]map[string]any, 0, len(infos))
		for _, i := range infos {
			entry := map[string]any{
				"tag":       i.Tag,
				"digest":    i.Digest,
				"size":      i.Size,
				"mediaType": i.MediaType,
				"platforms": i.Platforms,
				"layers":    i.Layers,
				"index":     i.IsIndex,
			}
			if !i.Created.IsZero() {
				entry["created"] = i.Created.UTC().Format(time.RFC3339)
			}
			if i.Err != nil {
				entry["error"] = i.Err.Error()
			}
			out = append(out, entry)
		}
		return writeJSON(map[string]any{"registry": client.Address(), "repository": repo, "tags": out})
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TAG\tDIGEST\tSIZE\tCREATED\tPLATFORMS")
	for _, i := range infos {
		if i.Err != nil {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", i.Tag, "-", "-", "-", "error: "+i.Err.Error())
			continue
		}
		created := "-"
		if !i.Created.IsZero() {
			created = i.Created.Local().Format("2006-01-02 15:04")
		}
		platforms := strings.Join(i.Platforms, ",")
		if platforms == "" {
			platforms = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			i.Tag, registry.ShortDigest(i.Digest), humanSize(i.Size), created, platforms)
	}
	return w.Flush()
}

// runInspect prints a manifest, and the image config alongside it.
func runInspect(ctx context.Context, opts *options, ref string) error {
	client, _, err := newClient(opts)
	if err != nil {
		return err
	}
	repo, reference, err := parseRef(ref)
	if err != nil {
		return err
	}
	m, err := client.Manifest(ctx, repo, reference)
	if err != nil {
		return err
	}

	if opts.jsonOut {
		out := map[string]any{
			"repository": repo,
			"reference":  reference,
			"digest":     m.Digest,
			"size":       m.Size(),
			"manifest":   json.RawMessage(m.Raw),
		}
		if !m.IsIndex() {
			if cfg, err := client.Blob(ctx, repo, m.Config.Digest); err == nil {
				out["config"] = json.RawMessage(cfg)
			}
		}
		return writeJSON(out)
	}

	fmt.Printf("Repository:  %s\n", repo)
	fmt.Printf("Reference:   %s\n", reference)
	fmt.Printf("Digest:      %s\n", m.Digest)
	fmt.Printf("Media type:  %s\n", m.MediaType)
	fmt.Printf("Total size:  %s (%d bytes)\n", humanSize(m.Size()), m.Size())
	if m.IsIndex() {
		fmt.Printf("Platforms:   %d\n", len(m.Manifests))
		for _, d := range m.Manifests {
			fmt.Printf("  %-24s %s  %s\n", d.Platform.String(), registry.ShortDigest(d.Digest), humanSize(d.Size))
		}
	} else {
		fmt.Printf("Layers:      %d\n", len(m.Layers))
		for i, l := range m.Layers {
			fmt.Printf("  %2d  %s  %s\n", i+1, registry.ShortDigest(l.Digest), humanSize(l.Size))
		}
	}
	fmt.Println()
	fmt.Println(string(m.Raw))
	return nil
}

// deletion groups the tags of one manifest that is about to be deleted.
type deletion struct {
	repo   string
	digest string
	tags   []string
	// requested lists the tags the user actually named; any other entry in
	// tags disappears as collateral and is called out before confirming.
	requested []string
}

// runRemove deletes the named images, or a whole repository with --all.
func runRemove(ctx context.Context, opts *options, refs []string) error {
	if opts.readOnly {
		return fmt.Errorf("--read-only is set, refusing to delete")
	}
	client, _, err := newClient(opts)
	if err != nil {
		return err
	}

	var deletions []deletion
	if opts.all {
		if len(refs) != 1 {
			return fmt.Errorf("rm --all takes exactly one repository")
		}
		deletions, err = planRepoDeletion(ctx, client, refs[0])
	} else {
		deletions, err = planRefDeletions(ctx, client, refs)
	}
	if err != nil {
		return err
	}
	if len(deletions) == 0 {
		fmt.Println("nothing to delete")
		return nil
	}

	// Show the plan before doing anything irreversible.
	verb := "Deleting"
	if opts.dryRun {
		verb = "Would delete"
	}
	fmt.Printf("%s %s from %s:\n", verb, plural(len(deletions), "manifest", "manifests"), client.Host())
	collateral := false
	for _, d := range deletions {
		extra := collateralTags(d)
		line := fmt.Sprintf("  %s:%s  %s", d.repo, strings.Join(d.tags, ","), registry.ShortDigest(d.digest))
		if len(extra) > 0 {
			collateral = true
			line += fmt.Sprintf("   (also untags %s)", strings.Join(extra, ","))
		}
		fmt.Println(line)
	}
	if collateral {
		fmt.Println("\nNote: a registry deletes by digest, so every tag pointing at the same")
		fmt.Println("manifest is removed together.")
	}

	if opts.dryRun {
		return nil
	}
	if !opts.yes {
		ok, err := confirm(fmt.Sprintf("\nDelete %s? [y/N] ", plural(len(deletions), "manifest", "manifests")))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("cancelled")
			return nil
		}
	}

	var failed int
	for _, d := range deletions {
		if err := client.DeleteManifest(ctx, d.repo, d.digest); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s:%s — %v\n", d.repo, strings.Join(d.tags, ","), err)
			continue
		}
		fmt.Printf("  deleted %s:%s\n", d.repo, strings.Join(d.tags, ","))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d deletions failed", failed, len(deletions))
	}
	fmt.Printf("\n%s deleted. Run the registry's garbage collector to reclaim the disk space.\n",
		plural(len(deletions), "manifest", "manifests"))
	return nil
}

// planRefDeletions resolves the named references to distinct manifests,
// folding in the other tags that share each digest.
func planRefDeletions(ctx context.Context, client *registry.Client, refs []string) ([]deletion, error) {
	// repository -> requested tags
	wanted := map[string][]string{}
	order := []string{}
	for _, ref := range refs {
		repo, reference, err := parseRef(ref)
		if err != nil {
			return nil, err
		}
		if _, seen := wanted[repo]; !seen {
			order = append(order, repo)
		}
		wanted[repo] = append(wanted[repo], reference)
	}

	var out []deletion
	for _, repo := range order {
		// One pass over the repository's tags tells us both the digest of
		// each requested tag and which other tags share it.
		allTags, err := client.Tags(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("listing tags of %s: %w", repo, err)
		}
		digests, _ := client.ResolveDigests(ctx, repo, allTags)

		byDigest := map[string][]string{}
		for tag, digest := range digests {
			byDigest[digest] = append(byDigest[digest], tag)
		}

		seen := map[string]bool{}
		for _, reference := range wanted[repo] {
			digest := reference
			if !strings.HasPrefix(reference, "sha256:") {
				var ok bool
				digest, ok = digests[reference]
				if !ok {
					// Not in the tag list: ask the registry directly, so a
					// tag added since the listing still resolves.
					resolved, err := client.ManifestDigest(ctx, repo, reference)
					if err != nil {
						return nil, fmt.Errorf("%s:%s — %w", repo, reference, err)
					}
					digest = resolved
				}
			}
			if seen[digest] {
				continue
			}
			seen[digest] = true

			tags := byDigest[digest]
			if len(tags) == 0 {
				tags = []string{reference}
			}
			sort.Strings(tags)
			out = append(out, deletion{
				repo:      repo,
				digest:    digest,
				tags:      tags,
				requested: wanted[repo],
			})
		}
	}
	return out, nil
}

// planRepoDeletion resolves every tag of a repository into distinct manifests.
func planRepoDeletion(ctx context.Context, client *registry.Client, repo string) ([]deletion, error) {
	tags, err := client.Tags(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("listing tags of %s: %w", repo, err)
	}
	if len(tags) == 0 {
		return nil, nil
	}
	digests, failures := client.ResolveDigests(ctx, repo, tags)
	for tag, err := range failures {
		fmt.Fprintf(os.Stderr, "drm: skipping %s:%s — %v\n", repo, tag, err)
	}

	byDigest := map[string][]string{}
	for tag, digest := range digests {
		byDigest[digest] = append(byDigest[digest], tag)
	}

	out := make([]deletion, 0, len(byDigest))
	for digest, tagList := range byDigest {
		sort.Strings(tagList)
		out = append(out, deletion{repo: repo, digest: digest, tags: tagList, requested: tagList})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].tags[0] < out[j].tags[0] })
	return out, nil
}

// collateralTags lists the tags that go away without having been named.
func collateralTags(d deletion) []string {
	requested := map[string]bool{}
	for _, r := range d.requested {
		requested[r] = true
	}
	var extra []string
	for _, t := range d.tags {
		if !requested[t] {
			extra = append(extra, t)
		}
	}
	return extra
}

// parseRef splits "repo:tag" or "repo@sha256:..." into its two halves. A bare
// repository name defaults to the "latest" tag, as the Docker CLI does.
func parseRef(ref string) (repo, reference string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty image reference")
	}
	if repo, digest, ok := strings.Cut(ref, "@"); ok {
		if repo == "" || digest == "" {
			return "", "", fmt.Errorf("malformed reference %q", ref)
		}
		return repo, digest, nil
	}
	if i := strings.LastIndex(ref, ":"); i > 0 {
		return ref[:i], ref[i+1:], nil
	}
	return ref, "latest", nil
}

// confirm asks a yes/no question on the terminal.
func confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		// A bare newline (or closed stdin) means "no".
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// humanSize mirrors the UI's size formatting for the headless output.
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
	if val >= 10 {
		return fmt.Sprintf("%.0f %s", val, units[exp])
	}
	return fmt.Sprintf("%.1f %s", val, units[exp])
}
