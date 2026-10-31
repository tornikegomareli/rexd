// Command fakerex is the program that stands in for rex in tests. See package fakerex.
package main

import (
	"fmt"
	"os"

	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func main() {
	dir := os.Getenv(fakerex.EnvDir)
	if dir == "" {
		fmt.Fprintln(os.Stderr, "fakerex: only tests run this program")
		os.Exit(2)
	}
	os.Exit(fakerex.Run(dir, os.Args[1:]))
}
