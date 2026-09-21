// Command drm is a terminal UI for browsing and pruning a container registry.
package main

import (
	"os"

	"github.com/jonaskpb/docker-registry-manager/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
