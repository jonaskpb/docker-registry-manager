package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// dockerConfig is the subset of ~/.docker/config.json we consume.
type dockerConfig struct {
	Auths       map[string]dockerAuth `json:"auths"`
	CredsStore  string                `json:"credsStore"`
	CredHelpers map[string]string     `json:"credHelpers"`
}

type dockerAuth struct {
	Auth          string `json:"auth"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
}

// DockerConfigPath returns the path of the Docker CLI config file, honouring
// DOCKER_CONFIG.
func DockerConfigPath() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

// CredentialsFromDockerConfig looks up stored credentials for a registry host,
// the same way `docker login` records them -- first any configured credential
// helper, then the inline auths entry. It returns empty Credentials (and no
// error) when the host is simply not logged in.
func CredentialsFromDockerConfig(host string) (Credentials, error) {
	path := DockerConfigPath()
	if path == "" {
		return Credentials{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, nil
		}
		return Credentials{}, err
	}
	var cfg dockerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Credentials{}, fmt.Errorf("malformed %s: %w", path, err)
	}

	// Docker Hub is stored under a legacy key.
	lookup := host
	if host == "registry-1.docker.io" || host == "docker.io" || host == "index.docker.io" {
		lookup = "https://index.docker.io/v1/"
	}

	if helper := credHelperFor(cfg, host); helper != "" {
		creds, err := runCredHelper(helper, lookup)
		if err == nil && !creds.Empty() {
			return creds, nil
		}
		// A helper that has no entry for this host is not an error; fall
		// through to the inline auths below.
	}

	for key, entry := range cfg.Auths {
		if !hostMatches(key, host) && key != lookup {
			continue
		}
		if entry.IdentityToken != "" {
			return Credentials{Token: entry.IdentityToken}, nil
		}
		if entry.Auth != "" {
			decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
			if err != nil {
				return Credentials{}, fmt.Errorf("malformed auth entry for %s in %s", key, path)
			}
			user, pass, ok := strings.Cut(string(decoded), ":")
			if !ok {
				return Credentials{}, fmt.Errorf("malformed auth entry for %s in %s", key, path)
			}
			return Credentials{Username: user, Password: pass}, nil
		}
		if entry.Username != "" || entry.Password != "" {
			return Credentials{Username: entry.Username, Password: entry.Password}, nil
		}
	}
	return Credentials{}, nil
}

// credHelperFor picks the per-host helper if configured, otherwise the global
// credential store.
func credHelperFor(cfg dockerConfig, host string) string {
	for key, helper := range cfg.CredHelpers {
		if hostMatches(key, host) {
			return helper
		}
	}
	return cfg.CredsStore
}

// runCredHelper invokes docker-credential-<helper> get, the protocol the
// Docker CLI uses: the server URL on stdin, a JSON credential on stdout.
func runCredHelper(helper, serverURL string) (Credentials, error) {
	bin := "docker-credential-" + helper
	if _, err := exec.LookPath(bin); err != nil {
		return Credentials{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "get")
	cmd.Stdin = strings.NewReader(serverURL)
	out, err := cmd.Output()
	if err != nil {
		return Credentials{}, fmt.Errorf("%s get: %w", bin, err)
	}
	var payload struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Credentials{}, fmt.Errorf("malformed output from %s: %w", bin, err)
	}
	// Helpers signal "token, not password" with this sentinel username.
	if payload.Username == "<token>" {
		return Credentials{Token: payload.Secret}, nil
	}
	return Credentials{Username: payload.Username, Password: payload.Secret}, nil
}

// hostMatches compares a docker config key (which may carry a scheme and a
// path) against a bare host[:port].
func hostMatches(key, host string) bool {
	k := key
	if i := strings.Index(k, "://"); i >= 0 {
		k = k[i+3:]
	}
	k = strings.TrimSuffix(strings.SplitN(k, "/", 2)[0], "/")
	return strings.EqualFold(k, host)
}
