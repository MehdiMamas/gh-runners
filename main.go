// Command gh-runners manages GitHub Actions self-hosted runners on this
// machine. It works standalone or as a GitHub CLI extension (gh runners).
package main

import (
	"os"

	"github.com/MehdiMamas/gh-runners/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
