package main

import (
	"os"

	"vaultctl/internal/vaultctl"
)

func main() {
	os.Exit(vaultctl.RunCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
