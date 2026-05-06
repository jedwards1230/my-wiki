package main

import (
	"os"

	"github.com/jedwards1230/my-wiki/internal/cli"
	"github.com/jedwards1230/my-wiki/internal/version"
)

func main() {
	cli.SetVersion(version.Value)
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
