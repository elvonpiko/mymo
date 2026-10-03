// Package manifest reads mymo.yaml: the minimal, intentional
// description of one application. mymo refuses to guess what it
// deploys — the manifest exists to eliminate guessing, so its
// schema stays intentionally small: a name, a source, the port the
// container listens on inside mymo's network, and how it is exposed.
//
// The parser is deliberately strict: flat keys only, no unknown
// keys, no duplicates, no nesting. Anything the schema cannot say
// is not inferred — it is refused with the exact reason.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FileName is the manifest's on-disk name.
const FileName = "mymo.yaml"

// keys the schema knows, with the field each must fill.
const (
	keyName       = "name"
	keyType       = "type"
	keyImage      = "image"
	keyDockerfile = "dockerfile"
	keyPort       = "port"
	keyDomain     = "domain"
	keyHealth     = "health"
)

// Manifest is the parsed mymo.yaml. Zero values mean the key was
// absent; Dockerfile defaults to the manifest's own directory.
type Manifest struct {
	Name       string
	Type       string // "image" or "dockerfile"
	Image      string
	Dockerfile string
	Port       int
	Domain     string
	Health     string
}

// Parse reads manifest text. Every refusal names the exact problem;
// nothing is defaulted into existence.
func Parse(text string) (Manifest, error) {
	var m Manifest
	seen := map[string]bool{}
	for lineNo, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line != raw && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
			return Manifest{}, fmt.Errorf("mymo.yaml:%d: nesting is not supported — the schema is flat", lineNo+1)
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return Manifest{}, fmt.Errorf("mymo.yaml:%d: expected key: value, got %q", lineNo+1, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			return Manifest{}, fmt.Errorf("mymo.yaml:%d: key %q appears twice", lineNo+1, key)
		}
		seen[key] = true
		switch key {
		case keyName, keyType, keyImage, keyDomain, keyHealth:
			// strings keep their meaning verbatim; quotes are
			// tolerated but stripped as decoration
			value = strings.Trim(value, `"'`)
			switch key {
			case keyName:
				m.Name = value
			case keyType:
				m.Type = value
			case keyImage:
				m.Image = value
			case keyDomain:
				m.Domain = value
			case keyHealth:
				m.Health = value
			}
		case keyDockerfile:
			m.Dockerfile = strings.Trim(value, `"'`)
		case keyPort:
			port, err := strconv.Atoi(value)
			if err != nil {
				return Manifest{}, fmt.Errorf("mymo.yaml:%d: port %q is not a number", lineNo+1, value)
			}
			m.Port = port
		default:
			return Manifest{}, fmt.Errorf("mymo.yaml:%d: unknown key %q — the schema is: name, type, image, dockerfile, port, domain, health", lineNo+1, key)
		}
	}

	// required keys: what a deploy cannot be guessed from
	for _, k := range []struct {
		key   string
		empty bool
	}{
		{keyName, m.Name == ""},
		{keyType, m.Type == ""},
	} {
		if k.empty {
			return Manifest{}, fmt.Errorf("mymo.yaml: %s is required", k.key)
		}
	}
	if m.Port == 0 {
		return Manifest{}, fmt.Errorf("mymo.yaml: port is required — the port the container listens on inside the network")
	}
	switch m.Type {
	case "image":
		if m.Image == "" {
			return Manifest{}, fmt.Errorf("mymo.yaml: type image requires an image")
		}
	case "dockerfile":
		if m.Dockerfile == "" {
			m.Dockerfile = "." // the manifest's own directory
		}
	default:
		return Manifest{}, fmt.Errorf("mymo.yaml: type %q is unsupported — supported: image, dockerfile", m.Type)
	}
	return m, nil
}

// Discover inspects a project directory. mymo.yaml is the source of
// truth when present. A Dockerfile without a manifest is deployable
// in principle but not in fact — mymo returns a draft manifest for
// the operator to complete rather than guessing values it cannot
// know. A directory with no supported source is refused with the
// modes mymo supports.
func Discover(dir string) (m Manifest, draft string, found string, err error) {
	raw, readErr := os.ReadFile(filepath.Join(dir, FileName))
	if readErr == nil {
		m, err := Parse(string(raw))
		if err != nil {
			return Manifest{}, "", FileName, err
		}
		return m, "", FileName, nil
	}
	if !os.IsNotExist(readErr) {
		return Manifest{}, "", "", fmt.Errorf("read %s: %w", FileName, readErr)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "Dockerfile")); statErr == nil {
		return Manifest{}, Draft(filepath.Base(dir)), "Dockerfile", nil
	}

	return Manifest{}, "", "", ErrNoSource
}

// ErrNoSource is returned when a directory holds nothing mymo can
// deploy from.
var ErrNoSource = fmt.Errorf("no supported deployment source in this directory — supported: a mymo.yaml manifest, or a Dockerfile mymo can draft one for")

// Draft renders the manifest mymo would start from for a Dockerfile
// project. It is a starting point for the operator to finish, never
// a config mymo silently deploys: the port especially is the app's
// to declare, and mymo does not guess it.
func Draft(name string) string {
	var b strings.Builder
	b.WriteString("# mymo application manifest\n")
	fmt.Fprintf(&b, "# mymo deploys exactly what this file says — nothing is guessed.\n")
	fmt.Fprintf(&b, "%s: %s\n", keyName, name)
	fmt.Fprintf(&b, "%s: dockerfile\n", keyType)
	fmt.Fprintf(&b, "%s: .\n", keyDockerfile)
	fmt.Fprintf(&b, "# port is the port your container listens on INSIDE mymo's network.\n")
	fmt.Fprintf(&b, "%s: 8080\n", keyPort)
	fmt.Fprintf(&b, "# %s: %s   # uncomment for public HTTP through caddy\n", keyDomain, "app.example.com")
	fmt.Fprintf(&b, "# %s: %s       # uncomment when the app has a health endpoint\n", keyHealth, "/healthz")
	return b.String()
}
