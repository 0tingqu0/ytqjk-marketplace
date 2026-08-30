package main

import (
	"os"

	"github.com/0tingqu0/ytqjk-marketplace/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
