// Package cli parses the command line and dispatches either into the terminal
// UI or into one of the headless sub-commands.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
	"github.com/jonaskpb/docker-registry-manager/internal/ui"
)

// options holds everything the flag set can set.
type options struct {
	address       string
	username      string
	password      string
	passwordStdin bool
	token         string
	insecure      bool
	plainHTTP     bool
	readOnly      bool
	dryRun        bool
	yes           bool
	all           bool
	jsonOut       bool
	timeout       time.Duration
	showVersion   bool
}

const usageText = `drm — a terminal UI for browsing and pruning a container registry

Usage:
  drm [flags] [registry]              browse the registry in the terminal UI
  drm ls [flags] [registry]           list repositories
  drm tags [flags] <repository>       list the tags of a repository
  drm inspect [flags] <repo>:<tag>    print a manifest
  drm rm [flags] <repo>:<tag>...      delete images
  drm rm --all [flags] <repository>   delete every image in a repository

The registry address may also come from the DRM_REGISTRY environment
variable. It defaults to localhost:5000.

Credentials are taken from --username/--password, then from DRM_USERNAME /
DRM_PASSWORD, and finally from ~/.docker/config.json — so a prior
"docker login" is enough.

Flags:
  -r, --registry <addr>   registry address (host[:port], or a full URL)
  -u, --username <name>   username
  -p, --password <pass>   password (prefer --password-stdin)
      --password-stdin    read the password from stdin
      --token <token>     use a bearer token instead of a username/password
      --insecure          skip TLS certificate verification
      --plain-http        talk HTTP instead of HTTPS
      --read-only         disable deletion entirely
      --dry-run           show what a delete would do, without sending it
  -y, --yes               do not ask for confirmation (rm only)
      --all               delete every image in the repository (rm only)
      --json              emit JSON (ls, tags and inspect)
      --timeout <dur>     per-request timeout (default 30s)
  -v, --version           print the version
  -h, --help              this message

Examples:
  drm                                     browse localhost:5000
  drm registry.example.com                browse a remote registry
  drm ls -r registry.example.com          list its repositories
  drm tags myapp                          list the tags of myapp
  drm rm myapp:v1.0.0 myapp:v1.0.1        delete two images
  drm rm --all --dry-run staging/scratch  preview deleting a whole repository

Keys inside the UI: <?> help, </> filter, <enter> drill down, <ctrl-d> delete.
`

// Run is the program entry point. It returns the process exit code.
func Run(args []string, version string) int {
	opts := &options{timeout: 30 * time.Second}

	fs := flag.NewFlagSet("drm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	stringVar(fs, &opts.address, "registry", "r")
	stringVar(fs, &opts.username, "username", "u")
	stringVar(fs, &opts.password, "password", "p")
	fs.BoolVar(&opts.passwordStdin, "password-stdin", false, "")
	fs.StringVar(&opts.token, "token", "", "")
	fs.BoolVar(&opts.insecure, "insecure", false, "")
	fs.BoolVar(&opts.plainHTTP, "plain-http", false, "")
	fs.BoolVar(&opts.readOnly, "read-only", false, "")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "")
	boolVar(fs, &opts.yes, "yes", "y")
	fs.BoolVar(&opts.all, "all", false, "")
	fs.BoolVar(&opts.jsonOut, "json", false, "")
	fs.DurationVar(&opts.timeout, "timeout", 30*time.Second, "")
	boolVar(fs, &opts.showVersion, "version", "v")

	positional, err := parseInterleaved(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, usageText)
			return 0
		}
		fmt.Fprintf(os.Stderr, "drm: %v\n\nRun 'drm --help' for usage.\n", err)
		return 2
	}

	if opts.showVersion {
		fmt.Println("drm " + version)
		return 0
	}

	command := "ui"
	var rest []string
	if len(positional) > 0 {
		switch positional[0] {
		case "ls", "list", "repos", "repositories":
			command, rest = "ls", positional[1:]
		case "tags", "tag":
			command, rest = "tags", positional[1:]
		case "rm", "delete", "del":
			command, rest = "rm", positional[1:]
		case "inspect", "describe", "manifest":
			command, rest = "inspect", positional[1:]
		case "help":
			fmt.Fprint(os.Stdout, usageText)
			return 0
		default:
			// No sub-command: a bare positional is the registry address.
			rest = positional
		}
	}

	ctx, cancel := signalContext()
	defer cancel()

	if err := dispatch(ctx, command, rest, opts, version); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		fmt.Fprintf(os.Stderr, "drm: %v\n", err)
		return 1
	}
	return 0
}

func dispatch(ctx context.Context, command string, args []string, opts *options, version string) error {
	switch command {
	case "ui":
		// A bare positional address ("drm registry.example.com").
		if len(args) > 1 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(args[1:], " "))
		}
		if len(args) == 1 && opts.address == "" {
			opts.address = args[0]
		}
		return runUI(ctx, opts, version)

	case "ls":
		if len(args) > 1 {
			return fmt.Errorf("ls takes at most one argument (a registry address)")
		}
		if len(args) == 1 && opts.address == "" {
			opts.address = args[0]
		}
		return runList(ctx, opts)

	case "tags":
		if len(args) != 1 {
			return errors.New("tags needs exactly one repository")
		}
		return runTags(ctx, opts, args[0])

	case "inspect":
		if len(args) != 1 {
			return errors.New("inspect needs exactly one image reference")
		}
		return runInspect(ctx, opts, args[0])

	case "rm":
		if len(args) == 0 {
			return errors.New("rm needs at least one image reference")
		}
		return runRemove(ctx, opts, args)
	}
	return fmt.Errorf("unknown command %q", command)
}

// runUI starts the terminal interface.
func runUI(ctx context.Context, opts *options, version string) error {
	client, creds, err := newClient(opts)
	if err != nil {
		return err
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return errors.New("the terminal UI needs a TTY — try 'drm ls' or 'drm tags <repo>' instead")
	}

	// Fail fast with a clear message rather than opening an empty UI that
	// only then reports it cannot reach the registry.
	pingCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		return err
	}

	return ui.Run(ui.Config{
		Client:   client,
		Username: creds.Username,
		Version:  version,
		ReadOnly: opts.readOnly,
		DryRun:   opts.dryRun,
	})
}

// newClient builds the registry client and resolves credentials.
func newClient(opts *options) (*registry.Client, registry.Credentials, error) {
	address := opts.address
	if address == "" {
		address = os.Getenv("DRM_REGISTRY")
	}
	if address == "" {
		address = "localhost:5000"
	}

	creds, err := resolveCredentials(opts, address)
	if err != nil {
		return nil, creds, err
	}

	client, err := registry.New(registry.Options{
		Address:     address,
		Credentials: creds,
		Insecure:    opts.insecure,
		PlainHTTP:   opts.plainHTTP,
		Timeout:     opts.timeout,
	})
	if err != nil {
		return nil, creds, err
	}
	return client, creds, nil
}

// resolveCredentials applies the precedence: explicit flags, then the
// environment, then whatever "docker login" left in ~/.docker/config.json.
func resolveCredentials(opts *options, address string) (registry.Credentials, error) {
	if opts.passwordStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return registry.Credentials{}, fmt.Errorf("reading password from stdin: %w", err)
		}
		opts.password = strings.TrimRight(string(data), "\r\n")
	}

	if opts.token == "" {
		opts.token = os.Getenv("DRM_TOKEN")
	}
	if opts.token != "" {
		return registry.Credentials{Token: opts.token}, nil
	}

	if opts.username == "" {
		opts.username = os.Getenv("DRM_USERNAME")
	}
	if opts.password == "" {
		opts.password = os.Getenv("DRM_PASSWORD")
	}
	if opts.username != "" || opts.password != "" {
		return registry.Credentials{Username: opts.username, Password: opts.password}, nil
	}

	host := address
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host = strings.SplitN(host, "/", 2)[0]

	creds, err := registry.CredentialsFromDockerConfig(host)
	if err != nil {
		// A broken docker config should not stop an anonymous browse.
		fmt.Fprintf(os.Stderr, "drm: ignoring docker credentials: %v\n", err)
		return registry.Credentials{}, nil
	}
	return creds, nil
}

// stringVar registers a string flag under both its long and short name.
func stringVar(fs *flag.FlagSet, p *string, long, short string) {
	fs.StringVar(p, long, "", "")
	fs.StringVar(p, short, "", "")
}

func boolVar(fs *flag.FlagSet, p *bool, long, short string) {
	fs.BoolVar(p, long, false, "")
	fs.BoolVar(p, short, false, "")
}

// parseInterleaved lets flags and positional arguments appear in any order,
// which the standard flag package does not do on its own ("drm ls -r host"
// would otherwise treat "-r host" as positional).
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// colorize is disabled when output is piped, so machine-read output stays
// clean.
func init() {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}
