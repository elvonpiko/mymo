package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/ssh"
)

// runNodeEdit changes how mymo reaches a node: host, port, user, and
// auth. The name, the applications, and the baseline record stay.
// The box itself is not touched. At least one flag must differ from
// the record, so a bare invocation cannot look like it did something.
func runNodeEdit(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(stderr, "usage: mymo node edit <name> [-host h] [-port p] [-user u] [-auth key|agent] [-key path]")
		return exitUsage
	}
	name := args[0]
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	cur, err := store.GetNode(name)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node edit: %v\n", err)
		return exitErr
	}

	fs := flag.NewFlagSet("mymo node edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	host := fs.String("host", cur.Host, "host or IP address")
	port := fs.Int("port", cur.Port, "SSH port")
	user := fs.String("user", cur.User, "SSH user")
	auth := fs.String("auth", string(cur.Auth), "auth method: key or agent")
	keyPath := fs.String("key", cur.KeyPath, "path to private key")
	rest, err := parseFlags(fs, args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "mymo node edit: %v\n", err)
		return exitUsage
	}
	if len(rest) != 0 {
		fmt.Fprintln(stderr, "usage: mymo node edit <name> [-host h] [-port p] [-user u] [-auth key|agent] [-key path]")
		return exitUsage
	}

	next := cur
	next.Host = *host
	next.Port = *port
	next.User = *user
	next.Auth = domain.AuthMethod(*auth)
	next.KeyPath = *keyPath
	if next.Auth == domain.AuthAgent {
		next.KeyPath = ""
	}
	if err := next.Validate(); err != nil {
		fmt.Fprintf(stderr, "mymo node edit: %v\n", err)
		return exitErr
	}
	if next.Auth == domain.AuthKey {
		if err := ssh.CheckKeyFile(next.KeyPath); err != nil {
			fmt.Fprintf(stderr, "mymo node edit: key file %q: %v\n", next.KeyPath, err)
			return exitErr
		}
	}
	if next == cur {
		fmt.Fprintf(stdout, "%s is unchanged.\n", cur.Name)
		return exitOK
	}
	if err := store.UpdateNode(next); err != nil {
		fmt.Fprintf(stderr, "mymo node edit: %v\n", err)
		return exitErr
	}
	fmt.Fprintf(stdout, "updated %s — %s@%s, %s auth. applications and the baseline record are unchanged. the box was not touched.\n",
		next.Name, next.User, next.Address(), next.Auth)
	return exitOK
}
