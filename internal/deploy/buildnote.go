package deploy

import (
	"strconv"
	"strings"
)

// buildFailNote pulls docker's own reason out of a failed build's
// output. BuildKit wraps the failure in ceremony: the header it
// opens with says nothing, the ERROR line names the failing
// command, and the step's last output lines carry the cause — a
// compile error, a DNS lookup, a missing go.sum entry. The
// operator needs the lines that say something, not the first line
// BuildKit ever printed.
func buildFailNote(out string) string {
	lines := strings.Split(out, "\n")
	errLine := ""
	errIdx := -1
	for i, raw := range lines {
		if strings.Contains(raw, "ERROR:") {
			errLine = stripStepPrefix(raw)
			errIdx = i
			break
		}
	}
	var causes []string
	for i := errIdx - 1; i >= 0 && len(causes) < 2; i-- {
		raw := strings.TrimSpace(lines[i])
		if raw == "" || strings.HasPrefix(raw, "----") || strings.HasPrefix(raw, ">") {
			continue
		}
		plain := stripStepPrefix(raw)
		if plain == "" || strings.HasPrefix(plain, "[") || strings.HasPrefix(plain, "DONE") ||
			strings.HasPrefix(plain, "CACHED") || strings.HasPrefix(plain, "ERROR") {
			break
		}
		// collected backward, shown in the order docker said them
		causes = append([]string{plain}, causes...)
	}
	if len(causes) > 0 {
		if errLine != "" {
			return strings.Join(causes, " ") + " — " + errLine
		}
		return strings.Join(causes, " ")
	}
	if errLine != "" {
		return errLine
	}
	return reasonLine(out)
}

// stripStepPrefix drops BuildKit's step number and timing from a
// line: "#12 30.92 go: ..." becomes "go: ...".
func stripStepPrefix(line string) string {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "#") {
		return s
	}
	rest := strings.TrimLeft(s, "#0123456789")
	fields := strings.Fields(rest)
	if len(fields) > 1 {
		if _, err := strconv.ParseFloat(fields[0], 64); err == nil {
			return strings.Join(fields[1:], " ")
		}
	}
	return strings.TrimSpace(rest)
}

// OwnTheBuildFailure states whose fix a failed build is, in plain
// words. A build that cannot reach the internet failed in the
// node's docker networking — the box's side of the wire. Everything
// else failed inside the project: its Dockerfile, its source, its
// dependencies. The operator deserves the sentence that says
// which side to work on instead of reverse-engineering an exit
// code.
func OwnTheBuildFailure(note string) string {
	l := strings.ToLower(note)
	network := []string{
		"lookup ", "dial tcp", "i/o timeout", "no route to host",
		"network is unreachable", "connection refused", "connection reset",
		"temporary failure in name resolution",
	}
	for _, w := range network {
		if strings.Contains(l, w) {
			return "the build could not reach the internet from the node — the box's docker networking, not the app's code"
		}
	}
	return "the failure is inside the project's build — its Dockerfile and its source; the fix is the project's, not the node's"
}
