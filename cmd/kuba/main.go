package main

import (
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/hcgatewood/kuba/internal/cli"
)

// version is set at link time via -ldflags "-X main.version=...", for builds with no git to read; cli.Main falls back
// to the build info's version, then dev.
var version = "dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], version))
}
