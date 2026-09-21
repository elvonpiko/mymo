// Command mymo manages a small fleet of Linux VPSes from the terminal.
package main

import (
	"context"
	"os"

	"github.com/elvonpiko/mymo/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
