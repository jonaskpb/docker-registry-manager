package registry_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
	"github.com/jonaskpb/docker-registry-manager/internal/regtest"
)

// newClient wires a client to a fake registry.
func newClient(t *testing.T, reg *regtest.Registry, creds registry.Credentials) *registry.Client {
	t.Helper()
	c, err := registry.New(registry.Options{
		Address:     reg.URL(),
		Credentials: creds,
		Timeout:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return c
}

func TestPingAndCatalog(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	reg.AddImage(regtest.Image{Repo: "alpine", Tag: "3.19", LayerSizes: []int64{3 << 20}})
	reg.AddImage(regtest.Image{Repo: "team/api", Tag: "v1", LayerSizes: []int64{10 << 20}})

	c := newClient(t, reg, registry.Credentials{})
	ctx := context.Background()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	repos, err := c.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	want := []string{"alpine", "team/api"}
	if strings.Join(repos, ",") != strings.Join(want, ",") {
		t.Errorf("Catalog = %v, want %v", repos, want)
	}
}

// A registry hands long listings back one page at a time; the client must
// follow the Link header until the listing is exhausted.
func TestCatalogAndTagsPagination(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	reg.PageSize = 3

	for i := 0; i < 10; i++ {
		reg.AddImage(regtest.Image{Repo: fmt.Sprintf("repo%02d", i), Tag: "latest", LayerSizes: []int64{1 << 20}})
	}
	for i := 0; i < 7; i++ {
		reg.AddImage(regtest.Image{Repo: "many", Tag: fmt.Sprintf("v%02d", i), LayerSizes: []int64{int64(i+1) << 20}})
	}

	c := newClient(t, reg, registry.Credentials{})
	ctx := context.Background()

	repos, err := c.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(repos) != 11 { // 10 repoNN plus "many"
		t.Errorf("Catalog returned %d repositories, want 11: %v", len(repos), repos)
	}

	tags, err := c.Tags(ctx, "many")
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 7 {
		t.Errorf("Tags returned %d tags, want 7: %v", len(tags), tags)
	}
}

func TestBasicAuth(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	reg.BasicAuth = &regtest.Creds{Username: "admin", Password: "s3cret"}
	reg.AddImage(regtest.Image{Repo: "private", Tag: "v1", LayerSizes: []int64{1 << 20}})

	ctx := context.Background()

	anon := newClient(t, reg, registry.Credentials{})
	if err := anon.Ping(ctx); err == nil {
		t.Fatal("Ping with no credentials should fail")
	} else if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("unhelpful error for bad auth: %v", err)
	}

	auth := newClient(t, reg, registry.Credentials{Username: "admin", Password: "s3cret"})
	if err := auth.Ping(ctx); err != nil {
		t.Fatalf("Ping with credentials: %v", err)
	}
	if _, err := auth.Catalog(ctx); err != nil {
		t.Fatalf("Catalog with credentials: %v", err)
	}
}

// The token handshake: the first request draws a 401 with a Bearer challenge,
// the client exchanges its basic credentials for a token at the realm, and
// retries.
func TestBearerTokenAuth(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	reg.BasicAuth = &regtest.Creds{Username: "robot", Password: "pw"}
	reg.TokenAuth = true
	reg.AddImage(regtest.Image{Repo: "app", Tag: "v1", LayerSizes: []int64{1 << 20}})

	c := newClient(t, reg, registry.Credentials{Username: "robot", Password: "pw"})
	ctx := context.Background()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping with token auth: %v", err)
	}
	repos, err := c.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog with token auth: %v", err)
	}
	if len(repos) != 1 || repos[0] != "app" {
		t.Errorf("Catalog = %v, want [app]", repos)
	}

	// The token is cached: a second call must not hit the token endpoint again.
	before := reg.Requests["GET /token"]
	if _, err := c.Tags(ctx, "app"); err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if after := reg.Requests["GET /token"]; after != before {
		t.Errorf("token endpoint hit %d extra times; the token should be cached", after-before)
	}
}

func TestManifestAndDigest(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	created := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	digest := reg.AddImage(regtest.Image{
		Repo: "app", Tag: "v1", Created: created,
		LayerSizes: []int64{5 << 20, 7 << 20},
	})

	c := newClient(t, reg, registry.Credentials{})
	ctx := context.Background()

	m, err := c.Manifest(ctx, "app", "v1")
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Digest != digest {
		t.Errorf("Manifest digest = %s, want %s", m.Digest, digest)
	}
	if len(m.Layers) != 2 {
		t.Errorf("got %d layers, want 2", len(m.Layers))
	}
	if m.IsIndex() {
		t.Error("single-platform manifest reported as an index")
	}
	// Size is the config blob plus both layers.
	if want := m.Config.Size + (5 << 20) + (7 << 20); m.Size() != want {
		t.Errorf("Size() = %d, want %d", m.Size(), want)
	}

	// HEAD must resolve the same digest without fetching the body.
	head, err := c.ManifestDigest(ctx, "app", "v1")
	if err != nil {
		t.Fatalf("ManifestDigest: %v", err)
	}
	if head != digest {
		t.Errorf("ManifestDigest = %s, want %s", head, digest)
	}

	cfg, err := c.ImageConfig(ctx, "app", m.Config)
	if err != nil {
		t.Fatalf("ImageConfig: %v", err)
	}
	if cfg.Created == nil || !cfg.Created.Equal(created.UTC()) {
		t.Errorf("config created = %v, want %v", cfg.Created, created.UTC())
	}
	if cfg.Platform().String() != "linux/amd64" {
		t.Errorf("platform = %s, want linux/amd64", cfg.Platform())
	}
}

func TestDescribeTagIndex(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	created := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	reg.AddIndex("multi", "v2", []string{"linux/amd64", "linux/arm64", "windows/amd64"}, created)

	c := newClient(t, reg, registry.Credentials{})
	info := c.DescribeTag(context.Background(), "multi", "v2")
	if info.Err != nil {
		t.Fatalf("DescribeTag: %v", info.Err)
	}
	if !info.IsIndex {
		t.Error("index not detected")
	}
	if got := strings.Join(info.Platforms, ","); got != "linux/amd64,linux/arm64,windows/amd64" {
		t.Errorf("platforms = %s", got)
	}
	// The creation time comes from a child manifest's config blob.
	if info.Created.IsZero() {
		t.Error("index has no creation time; the preferred child was not followed")
	}
	// The size must reflect the images the index points at, not the size of
	// the child manifest documents (a few hundred bytes each). Each child
	// here carries 1 MiB + 2 MiB of layers.
	const perChild = (1 << 20) + (2 << 20)
	if info.Size < 3*perChild {
		t.Errorf("index size = %d, want at least %d (the content of all three children)", info.Size, 3*perChild)
	}
}

func TestDeleteManifest(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	digest := reg.AddImage(regtest.Image{Repo: "app", Tag: "v1", LayerSizes: []int64{1 << 20}})
	reg.Tag("app", "latest", digest) // a second tag on the same manifest
	reg.AddImage(regtest.Image{Repo: "app", Tag: "v2", LayerSizes: []int64{2 << 20}})

	c := newClient(t, reg, registry.Credentials{})
	ctx := context.Background()

	if err := c.DeleteManifest(ctx, "app", digest); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	// Deleting a digest removes every tag that pointed at it.
	if got := strings.Join(reg.TagsOf("app"), ","); got != "v2" {
		t.Errorf("tags after delete = %q, want %q", got, "v2")
	}

	// Deleting again is reported as already gone, not as a mystery 404.
	err := c.DeleteManifest(ctx, "app", digest)
	if err == nil {
		t.Fatal("second delete should fail")
	}
	if !strings.Contains(err.Error(), "already gone") {
		t.Errorf("unhelpful error for a repeat delete: %v", err)
	}
}

// Deleting by tag is not something the API supports; the client must catch it
// before it reaches the network.
func TestDeleteByTagRefused(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	reg.AddImage(regtest.Image{Repo: "app", Tag: "v1", LayerSizes: []int64{1 << 20}})

	c := newClient(t, reg, registry.Credentials{})
	err := c.DeleteManifest(context.Background(), "app", "v1")
	if err == nil {
		t.Fatal("deleting by tag should be refused")
	}
	if !strings.Contains(err.Error(), "needs a digest") {
		t.Errorf("unhelpful error: %v", err)
	}
	if n := reg.Requests["DELETE /v2/app/manifests/v1"]; n != 0 {
		t.Errorf("client sent %d delete requests; it should not have sent any", n)
	}
}

// The most common deployment mistake: a registry started without
// REGISTRY_STORAGE_DELETE_ENABLED. The error must say so.
func TestDeleteDisabledExplains(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	reg.DeleteEnabled = false
	digest := reg.AddImage(regtest.Image{Repo: "app", Tag: "v1", LayerSizes: []int64{1 << 20}})

	c := newClient(t, reg, registry.Credentials{})
	err := c.DeleteManifest(context.Background(), "app", digest)
	if err == nil {
		t.Fatal("delete should fail when the registry has deletes disabled")
	}
	if !strings.Contains(err.Error(), "REGISTRY_STORAGE_DELETE_ENABLED") {
		t.Errorf("error does not name the fix: %v", err)
	}
}

func TestDescribeTagsStreamsEveryTag(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	var tags []string
	for i := 0; i < 25; i++ {
		tag := fmt.Sprintf("v%02d", i)
		tags = append(tags, tag)
		reg.AddImage(regtest.Image{Repo: "app", Tag: tag, LayerSizes: []int64{int64(i+1) << 20}})
	}

	c := newClient(t, reg, registry.Credentials{})
	seen := map[string]bool{}
	for info := range c.DescribeTags(context.Background(), "app", tags) {
		if info.Err != nil {
			t.Errorf("%s: %v", info.Tag, info.Err)
		}
		if seen[info.Tag] {
			t.Errorf("tag %s described twice", info.Tag)
		}
		seen[info.Tag] = true
	}
	if len(seen) != len(tags) {
		t.Errorf("described %d tags, want %d", len(seen), len(tags))
	}
}

// Cancelling must stop the stream rather than leaking goroutines that keep
// writing into a channel nobody reads.
func TestDescribeTagsRespectsCancellation(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	var tags []string
	for i := 0; i < 50; i++ {
		tag := fmt.Sprintf("v%02d", i)
		tags = append(tags, tag)
		reg.AddImage(regtest.Image{Repo: "app", Tag: tag, LayerSizes: []int64{1 << 20}})
	}

	c := newClient(t, reg, registry.Credentials{})
	ctx, cancel := context.WithCancel(context.Background())
	ch := c.DescribeTags(ctx, "app", tags)
	<-ch // take one result, then walk away
	cancel()

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DescribeTags did not stop after cancellation")
	}
}

func TestResolveDigests(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()
	d1 := reg.AddImage(regtest.Image{Repo: "app", Tag: "v1", LayerSizes: []int64{1 << 20}})
	reg.Tag("app", "stable", d1)
	d2 := reg.AddImage(regtest.Image{Repo: "app", Tag: "v2", LayerSizes: []int64{2 << 20}})

	c := newClient(t, reg, registry.Credentials{})
	digests, failures := c.ResolveDigests(context.Background(), "app", []string{"v1", "stable", "v2", "nope"})

	if digests["v1"] != d1 || digests["stable"] != d1 {
		t.Errorf("v1 and stable should share digest %s: got %s and %s", d1, digests["v1"], digests["stable"])
	}
	if digests["v2"] != d2 {
		t.Errorf("v2 digest = %s, want %s", digests["v2"], d2)
	}
	if _, ok := failures["nope"]; !ok {
		t.Error("a missing tag should be reported as a failure, not silently dropped")
	}
}

func TestErrorCarriesRegistryCode(t *testing.T) {
	reg := regtest.New()
	defer reg.Close()

	c := newClient(t, reg, registry.Credentials{})
	_, err := c.Tags(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("listing tags of a missing repository should fail")
	}
	var apiErr *registry.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not a *registry.Error: %T", err)
	}
	if !apiErr.Code("NAME_UNKNOWN") {
		t.Errorf("error does not carry NAME_UNKNOWN: %v", apiErr)
	}
}

func TestAddressNormalization(t *testing.T) {
	cases := []struct {
		in        string
		plainHTTP bool
		want      string
	}{
		{"registry.example.com", false, "https://registry.example.com"},
		{"registry.example.com:5000", false, "https://registry.example.com:5000"},
		{"localhost:5000", false, "http://localhost:5000"},
		{"127.0.0.1:5000", false, "http://127.0.0.1:5000"},
		{"https://registry.example.com/", false, "https://registry.example.com"},
		{"registry.example.com", true, "http://registry.example.com"},
		{"https://registry.example.com", true, "http://registry.example.com"},
	}
	for _, tc := range cases {
		c, err := registry.New(registry.Options{Address: tc.in, PlainHTTP: tc.plainHTTP})
		if err != nil {
			t.Errorf("New(%q): %v", tc.in, err)
			continue
		}
		if c.Address() != tc.want {
			t.Errorf("New(%q, plainHTTP=%v).Address() = %q, want %q", tc.in, tc.plainHTTP, c.Address(), tc.want)
		}
	}

	if _, err := registry.New(registry.Options{Address: ""}); err == nil {
		t.Error("an empty address should be rejected")
	}
}

func TestShortDigest(t *testing.T) {
	cases := map[string]string{
		"sha256:0123456789abcdef0123": "0123456789ab",
		"0123456789abcdef":            "0123456789ab",
		"short":                       "short",
		"":                            "",
	}
	for in, want := range cases {
		if got := registry.ShortDigest(in); got != want {
			t.Errorf("ShortDigest(%q) = %q, want %q", in, got, want)
		}
	}
}
