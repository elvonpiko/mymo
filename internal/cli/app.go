package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

func runApp(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "mymo app requires a subcommand.\n\n%s", appUsage())
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "list":
		return runAppList(args[1:], stdout, stderr)
	case "status":
		return runAppStatus(ctx, args[1:], stdout, stderr)
	case "logs":
		return runAppLogs(ctx, args[1:], stdout, stderr)
	case "restart":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: mymo app restart <name>")
			return exitUsage
		}
		return runAppLifecycle(ctx, "restart", args[1], stdout, stderr)
	case "stop":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: mymo app stop <name>")
			return exitUsage
		}
		return runAppLifecycle(ctx, "stop", args[1], stdout, stderr)
	case "start":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: mymo app start <name>")
			return exitUsage
		}
		return runAppLifecycle(ctx, "start", args[1], stdout, stderr)
	case "rollback":
		return runAppRollback(ctx, args[1:], stdout, stderr)
	case "shell":
		return runAppShell(ctx, args[1:], stdout, stderr)
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
	b.WriteString("  mymo app status <name>     the record and the live container\n")
	b.WriteString("  mymo app logs <name>       the active release's logs\n")
	b.WriteString("  mymo app restart <name>    restart the active release\n")
	b.WriteString("  mymo app stop <name>       stop the active release\n")
	b.WriteString("  mymo app start <name>      start the active release\n")
	b.WriteString("  mymo app rollback <name>   restore the previous release\n")
	b.WriteString("  mymo app shell <name>      a shell inside the container\n")
	return b.String()
}
