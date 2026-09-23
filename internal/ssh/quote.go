package ssh

import "strings"

// shellSafe matches the characters that are unambiguous inside a POSIX
// shell command line. Everything else gets quoted. Tilde and dollar are
// deliberately excluded: unquoted they expand, and mymo never wants a
// remote shell to reinterpret an argument.
const shellSafe = `ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_./:=,+@%-`

// shellQuote renders one argument so a POSIX shell parses it back to
// exactly the same bytes.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !strings.ContainsRune(shellSafe, r) {
			return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
		}
	}
	return s
}

// serialize renders an argument vector as a single command string for
// the remote shell. Every argument is quoted so metacharacters can
// never cross argument boundaries (spec: explicit argument vectors,
// never trust shell interpolation).
func serialize(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(name))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}
