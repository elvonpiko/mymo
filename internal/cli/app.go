package cli

import (
	"fmt"
	"io"
	"sort"
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
		return runAppList(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, appUsage())
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown app command %q\n\n%s", sub, appUsage())
		return exitUsage
	}
}

// runAppList prints every managed application: name, node, how it is
// exposed, and which release is live. The truth is the local record —
// it says what mymo last verified, not what the node is doing right
// now.
func runAppList(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: mymo app list")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	apps, err := store.LoadApps()
	if err != nil {
		fmt.Fprintf(stderr, "mymo: %v\n", err)
		return exitErr
	}
	if len(apps) == 0 {
		fmt.Fprintln(stdout, "No applications are managed by mymo yet.")
		return exitOK
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })

	nameW, nodeW := 0, 0
	for _, a := range apps {
		if len(a.Name) > nameW {
			nameW = len(a.Name)
		}
		if len(a.Node) > nodeW {
			nodeW = len(a.Node)
		}
	}
	for _, a := range apps {
		expose := a.Domain
		if expose == "" {
			expose = "internal only"
		}
		release := "no releases yet"
		if r, ok := a.ActiveRelease(); ok {
			digest := r.Digest
			if len(digest) > len("sha256:")+16 {
				digest = digest[:len("sha256:")+16]
			}
			release = fmt.Sprintf("release %d · %s · active", r.ID, digest)
		}
		fmt.Fprintf(stdout, "%-*s  %-*s  %-24s  %s\n",
			nameW, a.Name, nodeW, a.Node, expose, release)
	}
	return exitOK
}

func appUsage() string {
	var b strings.Builder
	b.WriteString("mymo app - manage applications\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  mymo app list    list managed applications\n")
	return b.String()
}
