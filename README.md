# docker-registry-manager

A k9s-style terminal UI for browsing and pruning a container registry.

`drm` talks to any registry that speaks the [Docker Registry HTTP API
V2](https://distribution.github.io/distribution/spec/api/) / [OCI distribution
spec](https://github.com/opencontainers/distribution-spec) — the CNCF
`registry:3` image, Harbor, GitLab, GHCR, ECR, Artifactory — and gives you a
keyboard-driven way to see what is in it and delete what is not needed any
more. It ships as a single static binary with no runtime dependencies.

```
Registry: http://localhost:5000  <enter> show tags                             ___  ___ __  __
User:     anonymous               <ctrl-d> delete repo                         |   \| _ \  \/  |
View:     repositories            <?> help                                     | |) |   / |\/| |
Mode:     read-write              <q> quit                                     |___/|_|_\_|  |_|
╭─ Repositories(5) ────────────────────────────────────────────────────────────────────────────╮
│  REPOSITORY↑                                                                             TAGS│
│  backend/api                                                                                4│
│  backend/worker                                                                             1│
│  frontend/web                                                                               2│
│  infra/nginx                                                                                1│
│  tools/migrate                                                                              1│
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
5 repositories in localhost:5000                                             REPOSITORY↑ · 1/5
```

Press `<enter>` on a repository to see its images:

```
Registry: http://localhost:5000  <enter> describe                              ___  ___ __  __
User:     anonymous               <ctrl-d> delete image                        |   \| _ \  \/  |
View:     tags · backend/api      <?> help                                     | |) |   / |\/| |
Mode:     read-write              <q> quit                                     |___/|_|_\_|  |_|
╭─ Images · backend/api(4) ────────────────────────────────────────────────────────────────────╮
│  TAG↑                           DIGEST             SIZE   AGE PLATFORMS                LAYERS│
│  latest                         fa4f51b931aa   60.0 MiB    2d linux/amd64                   3│
│  v2.3.7                         db897ef9a216   54.0 MiB   40d linux/amd64                   2│
│  v2.4.0                         bb4e43a9dd74   56.0 MiB    9d linux/amd64                   2│
│  v2.4.1                         fa4f51b931aa   60.0 MiB    2d linux/amd64                   3│
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
│                                                                                              │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
backend/api — 4 tags                                                                 TAG↑ · 1/4
```

## Why

`curl | jq` against `/v2/_catalog` tells you what a registry holds, but not how
big anything is, when it was built, or which tags are really the same image.
Deleting is worse: the API has no delete-by-tag, so you have to resolve a tag
to a manifest digest yourself — and then discover that every other tag on that
digest vanished too.

`drm` makes both of those a keystroke, and tells you what is about to happen
before it happens.

## Install

Download a binary for your platform from the
[releases page](https://github.com/jonaskpb/docker-registry-manager/releases),
or build it yourself:

```sh
go install github.com/jonaskpb/docker-registry-manager/cmd/drm@latest
```

From a checkout:

```sh
make build      # ./drm for this platform
make release    # dist/ with a static binary for linux, macOS and windows
```

Go 1.24 or newer. `CGO_ENABLED=0` is set for every build, so the result is one
statically linked file you can drop on a host and run.

## Usage

```sh
drm                              # browse localhost:5000
drm registry.example.com         # browse a remote registry
drm -r registry.example.com:5000 # same thing, explicitly
```

The address can also come from `DRM_REGISTRY`. A bare host defaults to HTTPS,
except for loopback addresses, which default to HTTP — the same convention the
Docker CLI uses for local development registries. Use `--plain-http` for a
non-TLS registry elsewhere, and `--insecure` to skip certificate verification.

### Keys

| Key | Action |
| --- | --- |
| `enter`, `d` | drill down: repositories → tags → image detail |
| `esc` | back one level, or clear the filter |
| `j`/`k`, `↑`/`↓` | move |
| `g` / `G` | first / last row |
| `ctrl-f` / `ctrl-b` | page down / up |
| `/` | filter — a regular expression, or plain text |
| `s`, `1`…`9` | sort by the next / n-th column (again to reverse) |
| `space` | mark a row, and move on |
| `ctrl-\`, `M` | clear all marks |
| `ctrl-d` | **delete** the marked images, or the one under the cursor |
| `y` | copy the image reference to the clipboard (OSC 52, works over SSH) |
| `r`, `ctrl-r` | refresh from the registry |
| `:` | command prompt — `:repos`, `:help`, `:refresh`, `:quit`, or a repository name |
| `?` | help |
| `q`, `ctrl-c` | quit |

### Deleting

Select an image and press `ctrl-d`. Mark several with `space` first to delete
them in one go, or press `ctrl-d` on a repository to clear it out entirely.

```
          ╭─────────────────────────────────────────────────────────────────────────╮
          │                                                                         │
          │   Delete 1 manifest                                                     │
          │                                                                         │
          │   Repository backend/api                                                │
          │                                                                         │
          │     latest, v2.4.1                      fa4f51b931aa                    │
          │                                                                         │
          │   ⚠ 1 other tag also points at this manifest and will be removed too.   │
          │   Blob storage is reclaimed only by the registry's garbage collector.   │
          │                                                                         │
          │   <y> or <enter> delete   <n> or <esc> cancel                           │
          │                                                                         │
          ╰─────────────────────────────────────────────────────────────────────────╯
```

Three things are worth knowing, and `drm` will remind you of all of them:

- **A registry deletes by digest, not by tag.** Every tag pointing at the same
  manifest disappears together. The confirmation lists them, and says how many
  you did not ask for.
- **Deleting frees no disk space by itself.** The registry only reclaims blobs
  when its garbage collector runs:
  `registry garbage-collect /etc/docker/registry/config.yml`.
- **Most registries refuse deletes until you turn them on**, with
  `REGISTRY_STORAGE_DELETE_ENABLED=true` (or `storage.delete.enabled` in the
  config file). If yours has not, the error says so rather than reporting a
  bare `405`.

Clearing a whole repository asks you to type its name — a single keystroke is
not enough for something that unrecoverable.

Two flags exist for when you want to be careful:

```sh
drm --dry-run     # work normally, but never send a delete
drm --read-only   # disable deleting entirely
```

### Authentication

Credentials are resolved in this order:

1. `--username` / `--password` (or `--password-stdin`), or `--token` for a
   bearer token
2. `DRM_USERNAME` / `DRM_PASSWORD` / `DRM_TOKEN`
3. `~/.docker/config.json`, including credential helpers

So if you have already run `docker login`, `drm` needs nothing else. Both
HTTP Basic and the Bearer token handshake (Docker Hub, GHCR, Harbor, GitLab)
are supported; tokens are cached per scope so browsing a repository does not
re-authenticate for every tag.

## Headless commands

The same functionality without the UI, for scripts and CI:

```sh
drm ls                                  # list repositories
drm ls --json
drm tags backend/api                    # tags with digest, size, age, platform
drm tags backend/api --json
drm inspect backend/api:v2.4.1          # manifest and image config
drm rm backend/api:v2.3.7               # delete, after showing the plan
drm rm backend/api:v2.3.7 --yes         # ...without asking
drm rm --all tools/migrate --dry-run    # preview clearing a repository
```

`rm` prints what it is about to delete — including any tag that shares a
digest with your target — and asks before doing it. `--yes` skips the prompt,
`--dry-run` skips the delete.

## How it works

| Package | Role |
| --- | --- |
| `internal/registry` | the distribution API client: auth, pagination, manifests, deletes |
| `internal/ui` | the Bubble Tea model, table widget and rendering |
| `internal/cli` | flag parsing and the headless sub-commands |
| `internal/regtest` | an in-memory registry the tests run against |

Listing a repository returns tags and nothing else, so sizes, platforms and
build dates need a manifest request per tag. `drm` issues those concurrently in
the background and streams each answer into the table as it lands: the list is
usable immediately and fills in as you look at it, and your cursor stays where
you put it.

## Development

```sh
make check    # go vet + tests
make race     # tests under the race detector
make test
```

The tests run against `internal/regtest`, an in-memory implementation of the
registry API covering pagination, the token handshake, content digests and
deletion semantics — so the suite needs no Docker daemon and no network.

## Licence

MIT. See [LICENSE](LICENSE).
