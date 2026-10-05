package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/facts"
	"github.com/elvonpiko/mymo/internal/ssh"
	"github.com/elvonpiko/mymo/internal/state"
	"github.com/elvonpiko/mymo/internal/tui"
)

// newForm builds a huh form themed with mymo's Catppuccin palette, so the
// CLI's standalone forms match the workspace.
func newForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithTheme(tui.HuhTheme())
}

func runNode(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "mymo node requires a subcommand.\n\n%s", nodeUsage())
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runNodeList(stdout, stderr)
	case "add":
		return runNodeAdd(rest, stdout, stderr)
	case "inspect":
		return runNodeInspect(rest, stdout, stderr)
	case "check":
		return runNodeCheck(ctx, rest, stdout, stderr)
	case "preflight":
		return runNodePreflight(ctx, rest, stdout, stderr)
	case "plan":
		return runNodePlan(ctx, rest, stdout, stderr)
	case "apply":
		return runNodeApply(ctx, rest, stdout, stderr)
	case "promote":
		return runNodePromote(ctx, rest, stdout, stderr)
	case "ssh":
		return runNodeSSH(rest, stderr)
	case "rm":
		return runNodeRemove(rest, stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, nodeUsage())
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown node command %q\n\n%s", sub, nodeUsage())
		return exitUsage
	}
}

func nodeUsage() string {
	var b strings.Builder
	b.WriteString("mymo node - manage nodes\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  mymo node list                list known nodes\n")
	b.WriteString("  mymo node add [flags]         add a node\n")
	b.WriteString("  mymo node inspect <name>      show a node's stored record\n")
	b.WriteString("  mymo node check <name>        probe a node over SSH and store what it finds\n")
	b.WriteString("  mymo node preflight <name>    read-only audit before preparing as app host\n")
	b.WriteString("  mymo node plan <name>         generate the preparation plan for review\n")
	b.WriteString("  mymo node apply <name>        apply the confirmed plan (typing the name confirms)\n")
	b.WriteString("  mymo node promote <name>      mark a verified node as app host\n")
	b.WriteString("  mymo node ssh <name>          open an interactive shell on the node\n")
	b.WriteString("  mymo node rm <name> [-f]      remove a node from local state\n")
	b.WriteString("\nFlags for \"mymo node add\":\n")
	b.WriteString("  -name string   node name (lowercase letters, digits, dashes)\n")
	b.WriteString("  -host string   host or IP address\n")
	b.WriteString("  -port int      SSH port (default 22)\n")
	b.WriteString("  -user string   SSH user\n")
	b.WriteString("  -auth string   auth method: key or agent\n")
	b.WriteString("  -key string    path to private key (required when -auth key)\n")
	b.WriteString("\nWith a terminal, \"mymo node add\" prompts interactively.\n")
	return b.String()
}

// openStore opens the default store, reporting failures to stderr.
func openStore(stderr io.Writer) (*state.Store, bool) {
	store, err := state.Open()
	if err != nil {
		fmt.Fprintf(stderr, "mymo: %v\n", err)
		return nil, false
	}
	return store, true
}

func runNodeList(stdout, stderr io.Writer) int {
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	nodes, err := store.LoadNodes()
	if err != nil {
		fmt.Fprintf(stderr, "mymo: load nodes: %v\n", err)
		return exitErr
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stdout, "No nodes yet.\nAdd one with: mymo node add")
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS\tUSER\tAUTH\tMODE")
	for _, n := range nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n.Name, n.Address(), n.User, n.Auth, n.Mode)
	}
	tw.Flush()
	return exitOK
}

func runNodeInspect(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node inspect <name>")
		return exitUsage
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	n, err := store.GetNode(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "mymo: %v\n", err)
		return exitErr
	}
	printNodeDetail(stdout, n)
	return exitOK
}

func printNodeDetail(w io.Writer, n domain.Node) {
	rows := [][2]string{
		{"name", n.Name},
		{"host", n.Host},
		{"port", strconv.Itoa(n.Port)},
		{"user", n.User},
		{"auth", string(n.Auth)},
	}
	if n.KeyPath != "" {
		rows = append(rows, [2]string{"key path", n.KeyPath})
	}
	rows = append(rows,
		[2]string{"mode", string(n.Mode)},
		[2]string{"added", n.AddedAt.Format(time.RFC3339)},
	)
	if n.LastCheck.At.IsZero() {
		rows = append(rows, [2]string{"last check", "never"})
	} else if n.LastCheck.Error != "" {
		rows = append(rows, [2]string{"last check", "failed " + facts.FormatAge(n.LastCheck.At) + ": " + n.LastCheck.Error})
	} else {
		rows = append(rows, [2]string{"last check", "ok " + facts.FormatAge(n.LastCheck.At)})
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-11s %s\n", r[0]+":", r[1])
	}
	if n.Facts.CollectedAt.IsZero() {
		return
	}
	fmt.Fprintln(w)
	printFacts(w, n.Facts)
}

// nodeInput gathers node fields from flags and the interactive form.
type nodeInput struct {
	name    string
	host    string
	port    int
	user    string
	auth    domain.AuthMethod
	keyPath string
}

// authPassword is the CLI's first-contact choice: it exists only in
// this package's flow, never in a domain record — node() refuses it,
// so no store file can ever hold it.
const authPassword = domain.AuthMethod("password")

// nodePasswordIn is where the password is read from: the terminal
// when a human runs the command, one line of a pipe when automation
// drives it. Injectable for tests.
var nodePasswordIn io.Reader = os.Stdin

// readNodePassword asks for the node's password — echo off at a
// terminal, a single line from a pipe. It is never a flag: flags sit
// in shell history. The value lives on the caller's stack and goes
// out of scope with it.
var readNodePassword = func(w io.Writer, user, host string) (string, error) {
	fmt.Fprintf(w, "password for %s@%s (never stored): ", user, host)
	if f, ok := nodePasswordIn.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(w)
		return string(b), err
	}
	line, err := bufio.NewReader(nodePasswordIn).ReadString('\n')
	fmt.Fprintln(w)
	return strings.TrimSpace(line), err
}

// cliFirstContact runs the password-to-key onboarding; injectable so
// the CLI tests never need a real node.
var cliFirstContact = ssh.FirstContact

// complete reports whether all required fields are present, listing the
// missing flag names otherwise. Port always has a default.
func (in nodeInput) complete() (bool, []string) {
	var missing []string
	if strings.TrimSpace(in.name) == "" {
		missing = append(missing, "-name")
	}
	if strings.TrimSpace(in.host) == "" {
		missing = append(missing, "-host")
	}
	if strings.TrimSpace(in.user) == "" {
		missing = append(missing, "-user")
	}
	switch in.auth {
	case "":
		missing = append(missing, "-auth")
	case domain.AuthKey:
		if strings.TrimSpace(in.keyPath) == "" {
			missing = append(missing, "-key")
		}
	case authPassword:
		// first contact: mymo generates the dedicated key itself
	}
	return len(missing) == 0, missing
}

// node builds and validates the domain record from the gathered input.
func (in nodeInput) node() (domain.Node, error) {
	if in.auth == authPassword {
		// the password choice must be resolved by first contact
		// before a record is built — a password can never reach a
		// persisted node
		return domain.Node{}, errors.New("internal: first contact must run before the node record is built")
	}
	n := domain.Node{
		Name:    strings.TrimSpace(in.name),
		Host:    strings.TrimSpace(in.host),
		Port:    in.port,
		User:    strings.TrimSpace(in.user),
		Auth:    in.auth,
		KeyPath: strings.TrimSpace(in.keyPath),
		Mode:    domain.ModeObserve,
		AddedAt: time.Now(),
	}
	if err := n.Validate(); err != nil {
		return domain.Node{}, err
	}
	return n, nil
}

func runNodeAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mymo node add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	name := fs.String("name", "", "node name")
	host := fs.String("host", "", "host or IP address")
	port := fs.Int("port", domain.DefaultSSHPort, "SSH port")
	user := fs.String("user", "", "SSH user")
	auth := fs.String("auth", "", "auth method: password (first contact), key, or agent")
	key := fs.String("key", "", "path to private key")
	positional, err := parseFlags(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node add: %v\n", err)
		return exitUsage
	}
	if len(positional) > 0 {
		fmt.Fprintf(stderr, "mymo node add: unexpected argument %q\n", positional[0])
		return exitUsage
	}
	in := nodeInput{
		name:    *name,
		host:    *host,
		port:    *port,
		user:    *user,
		auth:    domain.AuthMethod(*auth),
		keyPath: *key,
	}
	if complete, missing := in.complete(); !complete {
		if !stdinIsTerminal() {
			fmt.Fprintf(stderr,
				"mymo node add: missing required fields: %s\n\nProvide all fields via flags, or run \"mymo node add\" in a terminal for the interactive form.\n",
				strings.Join(missing, ", "))
			return exitUsage
		}
		if err := in.prompt(); err != nil {
			fmt.Fprintf(stderr, "mymo node add: %v\n", err)
			return exitErr
		}
	}
	if in.auth == authPassword {
		// first contact: connect once with the password, install a
		// dedicated key, prove it — only then is the node saved,
		// as key-auth. The password goes out of scope here.
		store, ok := openStore(stderr)
		if !ok {
			return exitErr
		}
		password, err := readNodePassword(stderr, in.user, in.host)
		if err != nil {
			fmt.Fprintf(stderr, "mymo node add: %v\n", err)
			return exitErr
		}
		if strings.TrimSpace(password) == "" {
			fmt.Fprintln(stderr, "mymo node add: the password is required for first contact")
			return exitErr
		}
		keyPath := ssh.FirstContactKeyPath(store.Dir(), in.name)
		fmt.Fprintf(stderr, "first contact with %s@%s — installing the key, proving it works…\n", in.user, in.host)
		if err := cliFirstContact(context.Background(), in.host, in.port, in.user, password, keyPath, knownHostsPath(store)); err != nil {
			if errors.Is(err, ssh.ErrPasswordExpired) {
				fmt.Fprintln(stderr, "mymo node add: the box demands a password change before the key can be installed.")
				fmt.Fprintln(stderr, "Ubuntu providers force a reset on first login. Change it once by hand:")
				fmt.Fprintf(stderr, "  ssh %s@%s\n", in.user, in.host)
				fmt.Fprintln(stderr, "then run the same mymo command again — first contact is idempotent.")
				return exitErr
			}
			fmt.Fprintf(stderr, "mymo node add: first contact failed, nothing was saved: %v\n", err)
			return exitErr
		}
		in.auth = domain.AuthKey
		in.keyPath = keyPath
	}
	node, err := in.node()
	if err != nil {
		fmt.Fprintf(stderr, "mymo node add: %v\n", err)
		return exitErr
	}
	if node.Auth == domain.AuthKey {
		if err := ssh.CheckKeyFile(node.KeyPath); err != nil {
			fmt.Fprintf(stderr, "mymo node add: key file %q: %v\n", node.KeyPath, err)
			return exitErr
		}
	}
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	if err := store.AddNode(node); err != nil {
		fmt.Fprintf(stderr, "mymo node add: %v\n", err)
		return exitErr
	}
	if node.Auth == domain.AuthKey && node.KeyPath == ssh.FirstContactKeyPath(store.Dir(), node.Name) {
		fmt.Fprintf(stdout, "First contact complete: %q is key-auth now — the password was never stored.\n", node.Name)
		fmt.Fprintf(stdout, "Added node %q in observe mode.\n", node.Name)
		return exitOK
	}
	fmt.Fprintf(stdout, "Added node %q in observe mode.\n", node.Name)
	return exitOK
}

// prompt asks interactively for any missing node fields.
func (in *nodeInput) prompt() error {
	portStr := strconv.Itoa(in.port)
	if in.port <= 0 {
		portStr = ""
	}
	auth := in.auth
	if auth == "" {
		auth = authPassword
	}
	form := newForm(
		huh.NewGroup(
			huh.NewInput().Title("Node name").Placeholder("web-1").Value(&in.name).
				Validate(func(s string) error { return domain.ValidateNodeName(s) }),
			huh.NewInput().Title("Host or IP address").Placeholder("203.0.113.10").Value(&in.host).
				Validate(nonEmpty("host is required")),
			huh.NewInput().Title("SSH port").Placeholder("22").Value(&portStr).
				Validate(portString),
			huh.NewInput().Title("SSH user").Placeholder("root").Value(&in.user).
				Validate(nonEmpty("user is required")),
			huh.NewSelect[domain.AuthMethod]().Title("How do you connect?").
				Options(
					huh.NewOption("Password — first contact (mymo installs a key)", authPassword),
					huh.NewOption("Private key file", domain.AuthKey),
					huh.NewOption("SSH agent", domain.AuthAgent),
				).Value(&auth),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}
	in.auth = auth
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil {
		return fmt.Errorf("invalid port %q", portStr)
	}
	in.port = port
	if in.auth == domain.AuthKey && strings.TrimSpace(in.keyPath) == "" {
		form := newForm(
			huh.NewGroup(
				huh.NewInput().Title("Private key path").Placeholder("/home/user/.ssh/id_ed25519").
					Value(&in.keyPath).
					Validate(nonEmpty("key path is required for key authentication")),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
	}
	return nil
}

func runNodeRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mymo node rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	force := fs.Bool("f", false, "remove without confirmation")
	positional, err := parseFlags(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "mymo node rm: %v\n", err)
		return exitUsage
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: mymo node rm <name> [-f]")
		return exitUsage
	}
	name := positional[0]
	store, ok := openStore(stderr)
	if !ok {
		return exitErr
	}
	if _, err := store.GetNode(name); err != nil {
		fmt.Fprintf(stderr, "mymo node rm: %v\n", err)
		return exitErr
	}
	if !*force {
		if !stdinIsTerminal() {
			fmt.Fprintln(stderr, "mymo node rm: confirmation requires a terminal; pass -f to remove without asking")
			return exitUsage
		}
		confirmed := false
		form := newForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Remove node %q from mymo? The server itself is not touched.", name)).
					Affirmative("remove").Negative("cancel").
					Value(&confirmed),
			),
		)
		if err := form.Run(); err != nil {
			fmt.Fprintf(stderr, "mymo node rm: %v\n", err)
			return exitErr
		}
		if !confirmed {
			fmt.Fprintln(stdout, "Aborted.")
			return exitOK
		}
	}
	if err := store.DeleteNode(name); err != nil {
		fmt.Fprintf(stderr, "mymo node rm: %v\n", err)
		return exitErr
	}
	fmt.Fprintf(stdout, "Removed node %q.\n", name)
	return exitOK
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// gatherFlags partitions args into flag tokens (each kept with the token
// that follows it when that token looks like the flag's value) and
// positionals, so that flags may also appear after positional arguments,
// which the flag package alone does not allow.
func gatherFlags(args []string) (flags, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a != "-" && strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return flags, rest
}

// parseFlags parses flags (anywhere in args) into fs and returns the
// remaining positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	flags, rest := gatherFlags(args)
	if err := fs.Parse(append(flags, rest...)); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func nonEmpty(msg string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(msg)
		}
		return nil
	}
}

func portString(s string) error {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return errors.New("port must be a number between 1 and 65535")
	}
	return nil
}
