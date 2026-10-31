// Command rexd installs plugins for Rex and keeps their event scripts running.
package main

import (
	"os"

	"github.com/tornikegomareli/rexd/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version string

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Options{Version: version}))
}
