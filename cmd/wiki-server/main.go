package main

import (
	"os"

	"github.com/jedwards1230/home-wiki/internal/cli"
)

var version = "dev"

func main() {
	cli.SetVersion(version)
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
