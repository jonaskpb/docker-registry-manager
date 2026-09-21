// Package regtest provides an in-memory implementation of the Docker Registry
// HTTP API V2, used to exercise the client and the UI without a real registry.
//
// It implements the parts drm depends on: the version check, catalog and tag
// listing with Link-header pagination, manifest GET/HEAD with content digests,
// blob reads, manifest deletion, and the optional token-auth handshake.
package regtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Registry is an in-memory registry.
type Registry struct {
	mu    sync.Mutex
	repos map[string]*repoState

	// DeleteEnabled mirrors REGISTRY_STORAGE_DELETE_ENABLED. When false,
	// deletes are rejected the way a default registry rejects them.
	DeleteEnabled bool
	// BasicAuth, when set, requires these credentials.
	BasicAuth *Creds
	// TokenAuth, when set, makes the registry answer 401 with a Bearer
	// challenge pointing at its own token endpoint.
	TokenAuth bool
	// PageSize caps how many entries a catalog or tags page returns,
	// exercising the client's pagination. Zero means no pagination.
	PageSize int

	// Requests counts served requests by "METHOD /path", for assertions.
	Requests map[string]int

	server *httptest.Server
}

// Creds is a username/password pair.
type Creds struct{ Username, Password string }

type repoState struct {
	manifests map[string][]byte // digest -> manifest bytes
	blobs     map[string][]byte // digest -> blob bytes
	tags      map[string]string // tag -> digest
}

// New starts a registry and returns it with its base URL.
func New() *Registry {
	r := &Registry{
		repos:         map[string]*repoState{},
		DeleteEnabled: true,
		Requests:      map[string]int{},
	}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	return r
}

// URL is the registry's base address.
func (r *Registry) URL() string { return r.server.URL }

// Host is the registry's host[:port].
func (r *Registry) Host() string { return strings.TrimPrefix(r.server.URL, "http://") }

// Close shuts the registry down.
func (r *Registry) Close() { r.server.Close() }

// Image describes an image to seed the registry with.
type Image struct {
	Repo    string
	Tag     string
	OS      string
	Arch    string
	Created time.Time
	// LayerSizes are the sizes reported for each layer descriptor.
	LayerSizes []int64
}

// AddImage seeds a single-platform image and returns its manifest digest.
// Adding the same content under a second tag reuses the manifest, which is
// what makes two tags share a digest.
func (r *Registry) AddImage(img Image) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.repo(img.Repo)

	if img.OS == "" {
		img.OS = "linux"
	}
	if img.Arch == "" {
		img.Arch = "amd64"
	}
	if img.Created.IsZero() {
		img.Created = time.Now().Add(-24 * time.Hour)
	}

	config := map[string]any{
		"created":      img.Created.UTC().Format(time.RFC3339),
		"architecture": img.Arch,
		"os":           img.OS,
		"config": map[string]any{
			"Env":        []string{"PATH=/usr/bin"},
			"Cmd":        []string{"/bin/sh"},
			"WorkingDir": "/",
		},
		"history": []map[string]any{
			{"created": img.Created.UTC().Format(time.RFC3339), "created_by": "RUN echo hello"},
		},
	}
	configBytes, _ := json.Marshal(config)
	configDigest := digestOf(configBytes)
	state.blobs[configDigest] = configBytes

	layers := make([]map[string]any, 0, len(img.LayerSizes))
	for i, size := range img.LayerSizes {
		layers = append(layers, map[string]any{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"size":      size,
			"digest":    digestOf([]byte(fmt.Sprintf("%s/%s/layer/%d", img.Repo, img.Tag, i))),
		})
	}

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"size":      len(configBytes),
			"digest":    configDigest,
		},
		"layers": layers,
	}
	manifestBytes, _ := json.Marshal(manifest)
	manifestDigest := digestOf(manifestBytes)

	state.manifests[manifestDigest] = manifestBytes
	if img.Tag != "" {
		state.tags[img.Tag] = manifestDigest
	}
	return manifestDigest
}

// AddIndex seeds a multi-platform index over the given platforms and returns
// its digest.
func (r *Registry) AddIndex(repo, tag string, platforms []string, created time.Time) string {
	var entries []map[string]any
	for _, p := range platforms {
		os, arch, _ := strings.Cut(p, "/")
		childDigest := r.AddImage(Image{
			Repo: repo, OS: os, Arch: arch, Created: created,
			LayerSizes: []int64{1 << 20, 2 << 20},
		})
		r.mu.Lock()
		size := len(r.repo(repo).manifests[childDigest])
		r.mu.Unlock()
		entries = append(entries, map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size":      size,
			"digest":    childDigest,
			"platform":  map[string]string{"os": os, "architecture": arch},
		})
	}

	index := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     entries,
	}
	indexBytes, _ := json.Marshal(index)
	indexDigest := digestOf(indexBytes)

	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.repo(repo)
	state.manifests[indexDigest] = indexBytes
	state.tags[tag] = indexDigest
	return indexDigest
}

// Tag points an additional tag at an existing digest.
func (r *Registry) Tag(repo, tag, digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repo(repo).tags[tag] = digest
}

// TagsOf returns the current tags of a repository, for assertions.
func (r *Registry) TagsOf(repo string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.repos[repo]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(state.tags))
	for tag := range state.tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// repo returns (creating if needed) the state of a repository. Callers hold
// the lock.
func (r *Registry) repo(name string) *repoState {
	state, ok := r.repos[name]
	if !ok {
		state = &repoState{
			manifests: map[string][]byte{},
			blobs:     map[string][]byte{},
			tags:      map[string]string{},
		}
		r.repos[name] = state
	}
	return state
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// --- HTTP ----------------------------------------------------------------

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.Requests[req.Method+" "+req.URL.Path]++
	r.mu.Unlock()

	if req.URL.Path == "/token" {
		r.serveToken(w, req)
		return
	}
	if !r.authorized(w, req) {
		return
	}

	switch {
	case req.URL.Path == "/v2" || req.URL.Path == "/v2/":
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "{}")
		return
	case req.URL.Path == "/v2/_catalog":
		r.serveCatalog(w, req)
		return
	}

	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case strings.HasSuffix(path, "/tags/list"):
		r.serveTags(w, req, strings.TrimSuffix(path, "/tags/list"))
	case strings.Contains(path, "/manifests/"):
		name, reference, _ := cutLast(path, "/manifests/")
		r.serveManifest(w, req, name, reference)
	case strings.Contains(path, "/blobs/"):
		name, digest, _ := cutLast(path, "/blobs/")
		r.serveBlob(w, req, name, digest)
	default:
		writeError(w, http.StatusNotFound, "UNSUPPORTED", "unknown endpoint")
	}
}

// cutLast splits around the last occurrence of sep, which is what correctly
// separates a repository name containing slashes from its reference.
func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// authorized enforces basic or token auth when configured.
func (r *Registry) authorized(w http.ResponseWriter, req *http.Request) bool {
	if r.BasicAuth == nil {
		return true
	}
	if r.TokenAuth {
		want := "Bearer " + r.expectedToken()
		if req.Header.Get("Authorization") == want {
			return true
		}
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer realm="%s/token",service="regtest",scope="repository:*:pull,push"`, r.server.URL))
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return false
	}

	user, pass, ok := req.BasicAuth()
	if ok && user == r.BasicAuth.Username && pass == r.BasicAuth.Password {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="regtest"`)
	writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	return false
}

func (r *Registry) expectedToken() string {
	return base64.RawURLEncoding.EncodeToString([]byte(r.BasicAuth.Username + ":" + r.BasicAuth.Password))
}

// serveToken is the token endpoint the Bearer challenge points at.
func (r *Registry) serveToken(w http.ResponseWriter, req *http.Request) {
	user, pass, ok := req.BasicAuth()
	if !ok || r.BasicAuth == nil || user != r.BasicAuth.Username || pass != r.BasicAuth.Password {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad credentials at token endpoint")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      r.expectedToken(),
		"expires_in": 300,
	})
}

func (r *Registry) serveCatalog(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	names := make([]string, 0, len(r.repos))
	for name, state := range r.repos {
		// A repository with no manifests left is gone, as in a real registry.
		if len(state.manifests) == 0 {
			continue
		}
		names = append(names, name)
	}
	r.mu.Unlock()
	sort.Strings(names)

	page, next := r.paginate(names, req)
	if next != "" {
		w.Header().Set("Link", fmt.Sprintf(`</v2/_catalog?last=%s&n=%d>; rel="next"`, next, r.PageSize))
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": page})
}

func (r *Registry) serveTags(w http.ResponseWriter, req *http.Request, name string) {
	r.mu.Lock()
	state, ok := r.repos[name]
	if !ok {
		r.mu.Unlock()
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	tags := make([]string, 0, len(state.tags))
	for tag := range state.tags {
		tags = append(tags, tag)
	}
	r.mu.Unlock()
	sort.Strings(tags)

	page, next := r.paginate(tags, req)
	if next != "" {
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?last=%s&n=%d>; rel="next"`, name, next, r.PageSize))
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "tags": page})
}

// paginate applies the registry's last/n pagination to a sorted slice.
func (r *Registry) paginate(items []string, req *http.Request) (page []string, next string) {
	if last := req.URL.Query().Get("last"); last != "" {
		for i, item := range items {
			if item == last {
				items = items[i+1:]
				break
			}
		}
	}
	size := r.PageSize
	if n, err := strconv.Atoi(req.URL.Query().Get("n")); err == nil && n > 0 && (size == 0 || n < size) {
		size = n
	}
	if size > 0 && len(items) > size {
		return items[:size], items[size-1]
	}
	return items, ""
}

func (r *Registry) serveManifest(w http.ResponseWriter, req *http.Request, name, reference string) {
	r.mu.Lock()
	state, ok := r.repos[name]
	if !ok {
		r.mu.Unlock()
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}

	digest := reference
	if !strings.HasPrefix(reference, "sha256:") {
		digest, ok = state.tags[reference]
		if !ok {
			r.mu.Unlock()
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
	}
	body, ok := state.manifests[digest]
	if !ok {
		r.mu.Unlock()
		writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	r.mu.Unlock()

	switch req.Method {
	case http.MethodGet, http.MethodHead:
		var parsed struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(body, &parsed)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Type", parsed.MediaType)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if req.Method == http.MethodGet {
			w.Write(body)
		}

	case http.MethodDelete:
		if !r.DeleteEnabled {
			writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "The operation is unsupported.")
			return
		}
		if !strings.HasPrefix(reference, "sha256:") {
			// Real registries refuse to delete by tag.
			writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "deleting by tag is not supported")
			return
		}
		r.mu.Lock()
		delete(state.manifests, digest)
		for tag, d := range state.tags {
			if d == digest {
				delete(state.tags, tag)
			}
		}
		r.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)

	default:
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
	}
}

func (r *Registry) serveBlob(w http.ResponseWriter, req *http.Request, name, digest string) {
	r.mu.Lock()
	state, ok := r.repos[name]
	var body []byte
	if ok {
		body, ok = state.blobs[digest]
	}
	r.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"errors": []map[string]any{{"code": code, "message": message}},
	})
}
