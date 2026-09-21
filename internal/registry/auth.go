package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Credentials are the static credentials used against a registry. An empty
// Credentials means anonymous access.
type Credentials struct {
	Username string
	Password string
	// Token, when set, is sent verbatim as a Bearer token and no token
	// exchange is attempted.
	Token string
}

// Empty reports whether no credential material is present at all.
func (c Credentials) Empty() bool {
	return c.Username == "" && c.Password == "" && c.Token == ""
}

// challenge is a parsed WWW-Authenticate header.
type challenge struct {
	scheme string            // "bearer" or "basic", lowercased
	params map[string]string // realm, service, scope, ...
}

// parseChallenge parses the first supported scheme out of a WWW-Authenticate
// header value, e.g.
//
//	Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:x:pull"
func parseChallenge(header string) *challenge {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	sp := strings.IndexAny(header, " \t")
	if sp < 0 {
		return &challenge{scheme: strings.ToLower(header), params: map[string]string{}}
	}
	c := &challenge{scheme: strings.ToLower(header[:sp]), params: map[string]string{}}
	for _, part := range splitParams(header[sp+1:]) {
		eq := strings.Index(part, "=")
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(part[:eq]))
		val := strings.TrimSpace(part[eq+1:])
		val = strings.Trim(val, `"`)
		c.params[key] = val
	}
	return c
}

// splitParams splits a comma-separated parameter list, ignoring commas that sit
// inside a quoted value (scope lists routinely contain them).
func splitParams(s string) []string {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
	)
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// bearerToken is a cached token with its expiry.
type bearerToken struct {
	token     string
	expiresAt time.Time
}

// authenticator turns a 401 challenge into an Authorization header, caching
// bearer tokens per scope. It is safe for concurrent use.
type authenticator struct {
	creds  Credentials
	client *http.Client

	mu     sync.Mutex
	tokens map[string]bearerToken // keyed by "service|scope"
}

func newAuthenticator(creds Credentials, client *http.Client) *authenticator {
	return &authenticator{creds: creds, client: client, tokens: map[string]bearerToken{}}
}

// authorize sets an Authorization header on req for the given challenge. A nil
// challenge means "use whatever static credentials we have", which covers
// registries that accept basic auth without advertising a challenge.
func (a *authenticator) authorize(ctx context.Context, req *http.Request, c *challenge) error {
	if a.creds.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.creds.Token)
		return nil
	}
	if c != nil && c.scheme == "bearer" {
		tok, err := a.bearer(ctx, c)
		if err != nil {
			return err
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		return nil
	}
	if a.creds.Username != "" || a.creds.Password != "" {
		req.SetBasicAuth(a.creds.Username, a.creds.Password)
	}
	return nil
}

// bearer returns a (possibly cached) token satisfying the challenge.
func (a *authenticator) bearer(ctx context.Context, c *challenge) (string, error) {
	realm := c.params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry sent a Bearer challenge without a realm")
	}
	key := c.params["service"] + "|" + c.params["scope"]

	a.mu.Lock()
	if tok, ok := a.tokens[key]; ok && time.Now().Before(tok.expiresAt) {
		a.mu.Unlock()
		return tok.token, nil
	}
	a.mu.Unlock()

	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("invalid token realm %q: %w", realm, err)
	}
	q := u.Query()
	if svc := c.params["service"]; svc != "" {
		q.Set("service", svc)
	}
	// A challenge can carry several space-separated scopes.
	if scope := c.params["scope"]; scope != "" {
		for _, s := range strings.Fields(scope) {
			q.Add("scope", s)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if a.creds.Username != "" || a.creds.Password != "" {
		req.SetBasicAuth(a.creds.Username, a.creds.Password)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &Error{StatusCode: resp.StatusCode, Method: req.Method, URL: u.String(), Body: string(body), Errors: parseAPIErrors(body)}
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		IssuedAt    string `json:"issued_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("malformed token response from %s: %w", u.Host, err)
	}
	tok := payload.Token
	if tok == "" {
		tok = payload.AccessToken
	}
	if tok == "" {
		return "", fmt.Errorf("token response from %s contained no token", u.Host)
	}

	// Registries commonly omit expires_in; the spec's default is 60s.
	ttl := time.Duration(payload.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	// Renew a little early so a token never expires mid-flight.
	if ttl > 10*time.Second {
		ttl -= 10 * time.Second
	}

	a.mu.Lock()
	a.tokens[key] = bearerToken{token: tok, expiresAt: time.Now().Add(ttl)}
	a.mu.Unlock()
	return tok, nil
}

// invalidate drops the cached token for a challenge, forcing a fresh exchange.
func (a *authenticator) invalidate(c *challenge) {
	if c == nil {
		return
	}
	a.mu.Lock()
	delete(a.tokens, c.params["service"]+"|"+c.params["scope"])
	a.mu.Unlock()
}

func parseAPIErrors(body []byte) []APIError {
	var payload struct {
		Errors []APIError `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	return payload.Errors
}
