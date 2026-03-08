package main

import (
	"github.com/herd-os/herd/internal/cli"
)

var version = "dev"

func main() {
	cli.SetVersion(version)
	cli.Execute()
}
