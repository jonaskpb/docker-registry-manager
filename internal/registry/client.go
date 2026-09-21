// Package registry is a small client for the Docker Registry HTTP API V2 /
// OCI distribution spec, covering the operations a registry browser needs:
// listing repositories and tags, reading manifests and image configs, and
// deleting manifests.
package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxBodySize caps how much of a response body is read. Manifests and image
// configs are small; anything larger is a sign we are talking to something
// that is not a registry.
const maxBodySize = 32 << 20 // 32 MiB

// defaultPageSize is the number of entries requested per catalog / tags page.
const defaultPageSize = 100

// Options configures a Client.
type Options struct {
	// Address is the registry address, with or without a scheme
	// ("registry.example.com", "https://registry.example.com:5000").
	Address string
	// Credentials authenticate against the registry. Zero value is anonymous.
	Credentials Credentials
	// Insecure skips TLS certificate verification. Separate from PlainHTTP.
	Insecure bool
	// PlainHTTP forces the http scheme when Address carries none.
	PlainHTTP bool
	// Timeout bounds a single HTTP request. Zero picks a sane default.
	Timeout time.Duration
	// UserAgent overrides the User-Agent header.
	UserAgent string
}

// Client talks to one registry. It is safe for concurrent use.
type Client struct {
	base      *url.URL
	http      *http.Client
	auth      *authenticator
	userAgent string

	// chalMu guards chal, the most recent auth challenge the registry sent.
	// Remembering it lets subsequent requests authenticate on their first
	// attempt instead of paying for a 401 round-trip each time -- which
	// matters when browsing a repository fires off a manifest request per tag.
	chalMu sync.RWMutex
	chal   *challenge
}

func (c *Client) loadChallenge() *challenge {
	c.chalMu.RLock()
	defer c.chalMu.RUnlock()
	return c.chal
}

func (c *Client) storeChallenge(ch *challenge) {
	c.chalMu.Lock()
	c.chal = ch
	c.chalMu.Unlock()
}

// New builds a Client for the given options.
func New(opts Options) (*Client, error) {
	base, err := normalizeAddress(opts.Address, opts.PlainHTTP)
	if err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.Insecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	// Registry browsing is request-heavy and highly concurrent; the stdlib
	// default of 2 idle connections per host would serialize it.
	transport.MaxIdleConnsPerHost = 16

	httpClient := &http.Client{Transport: transport, Timeout: timeout}

	ua := opts.UserAgent
	if ua == "" {
		ua = "drm/registry-client"
	}

	return &Client{
		base:      base,
		http:      httpClient,
		auth:      newAuthenticator(opts.Credentials, httpClient),
		userAgent: ua,
	}, nil
}

// normalizeAddress turns a user-supplied address into a base URL. A missing
// scheme defaults to https, except for loopback addresses (and when plainHTTP
// is set), which default to http -- the same convention the Docker CLI uses
// for local development registries.
func normalizeAddress(addr string, plainHTTP bool) (*url.URL, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, errors.New("no registry address given")
	}
	if !strings.Contains(addr, "://") {
		scheme := "https"
		if plainHTTP || isLoopback(addr) {
			scheme = "http"
		}
		addr = scheme + "://" + addr
	} else if plainHTTP {
		addr = "http://" + addr[strings.Index(addr, "://")+3:]
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid registry address %q: %w", addr, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid registry address %q: no host", addr)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func isLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Address returns the normalized registry base URL, e.g. https://reg:5000.
func (c *Client) Address() string { return c.base.String() }

// Host returns the registry host[:port], which is also the prefix of a pullable
// image reference.
func (c *Client) Host() string { return c.base.Host }

// url builds an absolute registry URL from a /v2 path and query parameters.
func (c *Client) url(path string, query url.Values) string {
	u := *c.base
	u.Path = c.base.Path + path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// response is a completed registry request whose body has been read.
type response struct {
	status int
	header http.Header
	body   []byte
}

// do issues a request, transparently handling the registry's 401 auth
// challenge: the first attempt discovers the challenge, the second carries the
// resulting Authorization header.
func (c *Client) do(ctx context.Context, method, rawURL string, accept []string) (*response, error) {
	chal := c.loadChallenge()

	for attempt := 0; attempt < 2; attempt++ {
		// Whether this attempt presents credentials derived from a challenge.
		// A 401 answering an attempt that presented none is the expected
		// opening move, not a sign that a cached token went bad.
		presented := chal != nil
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		for _, a := range accept {
			req.Header.Add("Accept", a)
		}
		if err := c.auth.authorize(ctx, req, chal); err != nil {
			return nil, err
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, friendlyTransportError(method, rawURL, err)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading response from %s: %w", rawURL, readErr)
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			next := parseChallenge(resp.Header.Get("WWW-Authenticate"))
			if next != nil {
				if presented {
					// We sent a token and it was refused: drop it, along with
					// any cached token for the scope now being demanded.
					c.auth.invalidate(chal)
					c.auth.invalidate(next)
				}
				c.storeChallenge(next)
				chal = next
				continue
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, &Error{
				StatusCode: resp.StatusCode,
				Method:     method,
				URL:        rawURL,
				Errors:     parseAPIErrors(body),
				Body:       string(body),
			}
		}
		return &response{status: resp.StatusCode, header: resp.Header, body: body}, nil
	}
	return nil, fmt.Errorf("authentication against %s failed", c.base.Host)
}

// friendlyTransportError rewrites the stdlib's noisy transport errors into
// something a user staring at a TUI status bar can act on.
func friendlyTransportError(method, rawURL string, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	u, perr := url.Parse(rawURL)
	host := rawURL
	if perr == nil {
		host = u.Host
	}
	var tlsErr *tls.CertificateVerificationError
	if errors.As(err, &tlsErr) {
		return fmt.Errorf("TLS verification failed for %s (use --insecure to skip, or --plain-http for a non-TLS registry): %w", host, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("timed out talking to %s", host)
	}
	if strings.Contains(err.Error(), "server gave HTTP response to HTTPS client") {
		return fmt.Errorf("%s speaks plain HTTP, not HTTPS (retry with --plain-http)", host)
	}
	return fmt.Errorf("%s %s: %w", method, host, err)
}

// Ping verifies that the address speaks the registry V2 API and that our
// credentials are accepted.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, c.url("/v2/", nil), nil)
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed for %s: check credentials (--username/--password, or docker login)", c.base.Host)
	}
	return err
}

// Catalog lists every repository in the registry, following the pagination
// Link headers until the registry stops handing out a next page.
func (c *Client) Catalog(ctx context.Context) ([]string, error) {
	var (
		repos []string
		next  = c.url("/v2/_catalog", url.Values{"n": {fmt.Sprint(defaultPageSize)}})
		seen  = map[string]bool{}
	)
	for next != "" {
		resp, err := c.do(ctx, http.MethodGet, next, []string{"application/json"})
		if err != nil {
			return nil, err
		}
		var page struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(resp.body, &page); err != nil {
			return nil, fmt.Errorf("malformed catalog response: %w", err)
		}
		for _, r := range page.Repositories {
			if !seen[r] {
				seen[r] = true
				repos = append(repos, r)
			}
		}
		link, err := c.nextPage(resp.header)
		if err != nil {
			return nil, err
		}
		// Guard against a registry that keeps handing back the same page.
		if link == next {
			break
		}
		next = link
	}
	return repos, nil
}

// Tags lists every tag of a repository, following pagination as Catalog does.
// A repository whose tags have all been deleted may legitimately report none.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	var (
		tags []string
		next = c.url("/v2/"+repo+"/tags/list", url.Values{"n": {fmt.Sprint(defaultPageSize)}})
		seen = map[string]bool{}
	)
	for next != "" {
		resp, err := c.do(ctx, http.MethodGet, next, []string{"application/json"})
		if err != nil {
			return nil, err
		}
		var page struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(resp.body, &page); err != nil {
			return nil, fmt.Errorf("malformed tags response for %s: %w", repo, err)
		}
		for _, t := range page.Tags {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		link, err := c.nextPage(resp.header)
		if err != nil {
			return nil, err
		}
		if link == next {
			break
		}
		next = link
	}
	return tags, nil
}

// nextPage extracts the rel="next" Link header and resolves it against the
// registry base URL. It returns "" when there is no further page.
func (c *Client) nextPage(h http.Header) (string, error) {
	link := h.Get("Link")
	if link == "" {
		return "", nil
	}
	for _, part := range splitParams(link) {
		part = strings.TrimSpace(part)
		open, close := strings.Index(part, "<"), strings.Index(part, ">")
		if open < 0 || close < open {
			continue
		}
		if !strings.Contains(strings.ToLower(part[close:]), `rel="next"`) {
			continue
		}
		ref, err := url.Parse(part[open+1 : close])
		if err != nil {
			return "", fmt.Errorf("malformed Link header %q: %w", link, err)
		}
		return c.base.ResolveReference(ref).String(), nil
	}
	return "", nil
}

// Manifest fetches a manifest by tag or digest. The returned Manifest carries
// its canonical digest and the raw bytes the registry served.
func (c *Client) Manifest(ctx context.Context, repo, reference string) (*Manifest, error) {
	resp, err := c.do(ctx, http.MethodGet, c.url("/v2/"+repo+"/manifests/"+reference, nil), []string{manifestAccept})
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(resp.body, &m); err != nil {
		return nil, fmt.Errorf("malformed manifest for %s:%s: %w", repo, reference, err)
	}
	m.Raw = resp.body
	m.Digest = resp.header.Get("Docker-Content-Digest")
	if m.Digest == "" {
		// Not every registry sets the header on GET; the digest is the
		// SHA-256 of the exact bytes served, so compute it ourselves.
		m.Digest = digestOf(resp.body)
	}
	if m.MediaType == "" {
		m.MediaType = resp.header.Get("Content-Type")
	}
	return &m, nil
}

// ManifestDigest resolves a tag to the digest a delete must address, using a
// HEAD request so the manifest body never crosses the wire.
func (c *Client) ManifestDigest(ctx context.Context, repo, reference string) (string, error) {
	resp, err := c.do(ctx, http.MethodHead, c.url("/v2/"+repo+"/manifests/"+reference, nil), []string{manifestAccept})
	if err != nil {
		return "", err
	}
	if d := resp.header.Get("Docker-Content-Digest"); d != "" {
		return d, nil
	}
	// Registries that omit the header on HEAD force a GET to get the bytes.
	m, err := c.Manifest(ctx, repo, reference)
	if err != nil {
		return "", err
	}
	if m.Digest == "" {
		return "", fmt.Errorf("registry did not report a digest for %s:%s", repo, reference)
	}
	return m.Digest, nil
}

// Blob fetches a blob by digest. Used for image config blobs, which are small.
func (c *Client) Blob(ctx context.Context, repo, digest string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, c.url("/v2/"+repo+"/blobs/"+digest, nil), []string{"*/*"})
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

// ImageConfig fetches and decodes the image config blob a manifest points at.
func (c *Client) ImageConfig(ctx context.Context, repo string, config Descriptor) (*ImageConfig, error) {
	if config.Digest == "" {
		return nil, errors.New("manifest carries no config descriptor")
	}
	raw, err := c.Blob(ctx, repo, config.Digest)
	if err != nil {
		return nil, err
	}
	var cfg ImageConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("malformed image config %s: %w", shortDigest(config.Digest), err)
	}
	return &cfg, nil
}

// DeleteManifest deletes a manifest by digest. The registry API has no
// delete-by-tag: deleting a digest untags *every* tag that resolves to it, and
// the underlying blobs are only reclaimed by a subsequent garbage collection.
func (c *Client) DeleteManifest(ctx context.Context, repo, digest string) error {
	if !strings.Contains(digest, ":") {
		return fmt.Errorf("refusing to delete %q: a manifest delete needs a digest, not a tag", digest)
	}
	_, err := c.do(ctx, http.MethodDelete, c.url("/v2/"+repo+"/manifests/"+digest, nil), nil)
	if err == nil {
		return nil
	}
	return explainDeleteError(err, repo, digest)
}

// explainDeleteError turns the two failure modes users actually hit -- deletes
// disabled server-side, and an already-deleted manifest -- into plain language.
func explainDeleteError(err error, repo, digest string) error {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.StatusCode == http.StatusMethodNotAllowed || apiErr.Code("UNSUPPORTED"):
		return fmt.Errorf("this registry has deletes disabled (set REGISTRY_STORAGE_DELETE_ENABLED=true, or storage.delete.enabled in its config)")
	case apiErr.StatusCode == http.StatusNotFound || apiErr.Code("MANIFEST_UNKNOWN"):
		return fmt.Errorf("%s@%s is already gone", repo, shortDigest(digest))
	case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden:
		return fmt.Errorf("not permitted to delete from %s: the credentials need push/delete scope", repo)
	}
	return err
}
