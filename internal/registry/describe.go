package registry

import (
	"context"
	"sort"
	"sync"
	"time"
)

// TagInfo is everything the UI shows about a single tag. Fields beyond Repo
// and Tag are filled in by DescribeTag and may be zero if the registry refused
// the lookup, in which case Err says why.
type TagInfo struct {
	Repo      string
	Tag       string
	Digest    string
	MediaType string
	Size      int64
	Created   time.Time
	Platforms []string
	Layers    int
	IsIndex   bool
	Err       error
}

// Ref renders the pullable reference for this tag, e.g. reg:5000/app:v1.
func (t TagInfo) Ref(host string) string {
	return host + "/" + t.Repo + ":" + t.Tag
}

// DigestRef renders the digest-pinned reference, e.g. reg:5000/app@sha256:...
func (t TagInfo) DigestRef(host string) string {
	if t.Digest == "" {
		return t.Ref(host)
	}
	return host + "/" + t.Repo + "@" + t.Digest
}

// preferredPlatforms orders index entries so the platform most users care
// about is the one we spend a config lookup on.
var preferredPlatforms = []string{"linux/amd64", "linux/arm64"}

// DescribeTag resolves one tag into a TagInfo: its digest, media type, total
// size, platforms and creation time. For a multi-platform index it reads one
// child manifest to recover the creation time, preferring linux/amd64.
func (c *Client) DescribeTag(ctx context.Context, repo, tag string) TagInfo {
	info := TagInfo{Repo: repo, Tag: tag}

	m, err := c.Manifest(ctx, repo, tag)
	if err != nil {
		info.Err = err
		return info
	}
	info.Digest = m.Digest
	info.MediaType = m.MediaType
	info.Size = m.Size()
	info.IsIndex = m.IsIndex()

	if !m.IsIndex() {
		info.Layers = len(m.Layers)
		if p := platformOfManifest(m); p != "" {
			info.Platforms = []string{p}
		}
		if cfg, err := c.ImageConfig(ctx, repo, m.Config); err == nil {
			if cfg.Created != nil {
				info.Created = *cfg.Created
			}
			if p := cfg.Platform(); p != nil {
				info.Platforms = []string{p.String()}
			}
		}
		return info
	}

	// Multi-platform index. The index's own descriptors size the child
	// manifest documents, not the images, so the real storage figure needs
	// each child manifest fetched and summed.
	var (
		children []Descriptor
		pick     = -1
	)
	for i := range m.Manifests {
		d := m.Manifests[i]
		// Attestation manifests (buildkit) are listed with an "unknown"
		// platform; they are noise in a platform column, but they do occupy
		// storage, so they stay in the size total.
		if d.Platform == nil || d.Platform.OS != "unknown" {
			if p := d.Platform.String(); p != "" && p != "/" {
				info.Platforms = append(info.Platforms, p)
			}
			if pick < 0 || (isPreferred(d.Platform) && !isPreferred(children[pick].Platform)) {
				pick = len(children)
			}
		}
		children = append(children, d)
	}
	sort.Strings(info.Platforms)

	info.Size = c.sumChildren(ctx, repo, children)

	if pick >= 0 {
		if child, err := c.Manifest(ctx, repo, children[pick].Digest); err == nil {
			info.Layers = len(child.Layers)
			if cfg, err := c.ImageConfig(ctx, repo, child.Config); err == nil && cfg.Created != nil {
				info.Created = *cfg.Created
			}
		}
	}
	return info
}

// sumChildren totals the content size of an index's child manifests. A child
// that cannot be read contributes its descriptor size, so the total degrades
// instead of collapsing to zero.
func (c *Client) sumChildren(ctx context.Context, repo string, children []Descriptor) int64 {
	var (
		mu    sync.Mutex
		total int64
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 4)
	)
	for _, d := range children {
		wg.Add(1)
		sem <- struct{}{}
		go func(d Descriptor) {
			defer wg.Done()
			defer func() { <-sem }()

			size := d.Size
			if child, err := c.Manifest(ctx, repo, d.Digest); err == nil && !child.IsIndex() {
				size = child.Size()
			}
			mu.Lock()
			total += size
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	return total
}

func isPreferred(p *Platform) bool {
	if p == nil {
		return false
	}
	s := p.OS + "/" + p.Architecture
	for _, pref := range preferredPlatforms {
		if s == pref {
			return true
		}
	}
	return false
}

// platformOfManifest recovers a platform from a manifest's own annotations,
// used when the config blob is unreachable.
func platformOfManifest(m *Manifest) string {
	os, arch := m.Annotations["org.opencontainers.image.os"], m.Annotations["org.opencontainers.image.architecture"]
	if os == "" || arch == "" {
		return ""
	}
	return os + "/" + arch
}

// DescribeConcurrency bounds how many tag lookups run at once. Registries are
// happy to serve these in parallel, but a few hundred in flight is impolite.
const DescribeConcurrency = 8

// DescribeTags resolves many tags concurrently, emitting each result on the
// returned channel as soon as it lands so a UI can fill its table
// progressively. The channel closes when every tag has been described or ctx
// is cancelled.
func (c *Client) DescribeTags(ctx context.Context, repo string, tags []string) <-chan TagInfo {
	out := make(chan TagInfo)
	go func() {
		defer close(out)
		sem := make(chan struct{}, DescribeConcurrency)
		var wg sync.WaitGroup
		for _, tag := range tags {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(tag string) {
				defer wg.Done()
				defer func() { <-sem }()
				info := c.DescribeTag(ctx, repo, tag)
				select {
				case out <- info:
				case <-ctx.Done():
				}
			}(tag)
		}
		wg.Wait()
	}()
	return out
}

// RepoInfo summarizes a repository for the repository list.
type RepoInfo struct {
	Name string
	Tags int
	Err  error
}

// DescribeRepos counts the tags of each repository concurrently, emitting
// results as they arrive.
func (c *Client) DescribeRepos(ctx context.Context, repos []string) <-chan RepoInfo {
	out := make(chan RepoInfo)
	go func() {
		defer close(out)
		sem := make(chan struct{}, DescribeConcurrency)
		var wg sync.WaitGroup
		for _, repo := range repos {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(repo string) {
				defer wg.Done()
				defer func() { <-sem }()
				info := RepoInfo{Name: repo}
				tags, err := c.Tags(ctx, repo)
				if err != nil {
					info.Err = err
				} else {
					info.Tags = len(tags)
				}
				select {
				case out <- info:
				case <-ctx.Done():
				}
			}(repo)
		}
		wg.Wait()
	}()
	return out
}

// ResolveDigests maps each tag of a repository to its manifest digest, using
// HEAD requests in parallel. Tags whose digest cannot be resolved are omitted
// and reported in the errors map.
func (c *Client) ResolveDigests(ctx context.Context, repo string, tags []string) (map[string]string, map[string]error) {
	var (
		mu      sync.Mutex
		digests = make(map[string]string, len(tags))
		errs    = map[string]error{}
		sem     = make(chan struct{}, DescribeConcurrency)
		wg      sync.WaitGroup
	)
	for _, tag := range tags {
		wg.Add(1)
		sem <- struct{}{}
		go func(tag string) {
			defer wg.Done()
			defer func() { <-sem }()
			digest, err := c.ManifestDigest(ctx, repo, tag)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[tag] = err
				return
			}
			digests[tag] = digest
		}(tag)
	}
	wg.Wait()
	return digests, errs
}
