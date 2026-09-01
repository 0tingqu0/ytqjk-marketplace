package main

import (
	"os"

	"github.com/0tingqu0/ytqjk-marketplace/internal/cli"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
)

func main() {
	if handled, code := runtimeentry.Dispatch(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
