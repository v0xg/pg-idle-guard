package main

import (
	"os"

	"github.com/v0xg/pg-idle-guard/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
