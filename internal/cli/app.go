package cli

import (
	"fmt"
	"io"
	"strings"
)

func runApp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "mymo app requires a subcommand.\n\n%s", appUsage())
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "list":
		fmt.Fprintln(stdout, "No applications are managed by mymo yet.")
		return exitOK
	case "help", "--help", "-h":
		fmt.Fprint(stdout, appUsage())
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown app command %q\n\n%s", sub, appUsage())
		return exitUsage
	}
}

func appUsage() string {
	var b strings.Builder
	b.WriteString("mymo app - manage applications\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  mymo app list    list managed applications\n")
	return b.String()
}
