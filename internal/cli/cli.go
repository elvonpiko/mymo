// Package cli implements mymo's command-line interface. It is a thin
// presentation layer: behavior lives in the domain and state packages.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/elvonpiko/mymo/internal/tui"
	"github.com/elvonpiko/mymo/internal/version"
)

// Exit codes returned by Run.
const (
	exitOK    = 0 // success
	exitErr   = 1 // runtime error
	exitUsage = 2 // usage error
)

// Run executes mymo with the given arguments and returns the process exit
// code. It never calls os.Exit and never touches os.Stdout/os.Stderr
// directly, so it stays testable.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	_ = ctx // reserved for cancellable remote operations
	if len(args) == 0 {
		return runTUI(stderr)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage())
		return exitOK
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "mymo "+version.Version)
		return exitOK
	case "node":
		return runNode(rest, stdout, stderr)
	case "app":
		return runApp(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage())
		return exitUsage
	}
}

// usage returns mymo's top-level help text.
func usage() string {
	var b strings.Builder
	b.WriteString("mymo - manage a fleet of VPSes from the terminal\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  mymo                      fleet TUI (interactive)\n")
	b.WriteString("  mymo node <command>       manage nodes\n")
	b.WriteString("  mymo app <command>        manage applications\n")
	b.WriteString("  mymo version              print version\n")
	b.WriteString("  mymo help                 print help\n")
	return b.String()
}

// runTUI launches the interactive fleet TUI for bare "mymo" invocations.
func runTUI(stderr io.Writer) int {
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	if err := tui.Run(store); err != nil {
		if errors.Is(err, tui.ErrNoTerminal) {
			fmt.Fprintln(stderr, `mymo: the TUI needs an interactive terminal; use "mymo node list" for non-interactive output`)
			return exitErr
		}
		fmt.Fprintf(stderr, "mymo: %v\n", err)
		return exitErr
	}
	return exitOK
}
