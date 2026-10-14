package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Compose support, per the spec's compose model: mymo does not
// replace Docker Compose. Compose keeps the container semantics —
// services, dependencies, volumes, environment, health checks — and
// executes the file verbatim; mymo owns the production lifecycle
// around it: release identity, the network, exposure, health
// gating, rollback. This parser therefore reads a compose file for
// two jobs only: to name what mymo must know (the services, which
// of them build, which of them is exposed), and to refuse what
// breaks mymo's model. Everything else passes through untouched —
// mymo never rewrites the operator's file; it adds its own override
// beside it.
//
// The refusals are the spec's warning list, made hard where the
// model demands it:
//
//   - published ports — caddy is the only public door; a compose
//     ports: mapping would open doors beside it
//   - host networking, privileged mode, host pid/ipc namespaces,
//     added capabilities, devices — a container that reaches past
//     its sandbox is not one mymo can reason about
//   - binds to absolute host paths — the box's filesystem outside
//     the project is not the app's to touch; named volumes and
//     relative binds carry its data
//   - foreign networks — the app joins mymo's own network; any
//     network definition with a driver, or an external network that
//     is not mymo's, is refused rather than guessed at

// ComposeFile is what mymo learned from one compose file: the
// services by name, which of them carry a build, and the notes
// worth showing on the plan page.
type ComposeFile struct {
	Services []string          // definition order, for choosing the exposed service
	Builds   map[string]bool   // services that build from the transferred context
	Images   map[string]string // services that run a registry image, by name
	Warnings []string          // honest notes for the plan; never silently ignored
}

// ComposeFileName lists the file names discovery accepts, in
// precedence order.
var ComposeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// ConfigDigest is a compose release's identity: the digest of the
// exact configuration every service of the release runs — the
// compose equivalent of an image digest. Two releases with the same
// digest are the same release; any difference is a new one.
func ConfigDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:32])
}

// ParseCompose vets one compose file. The error names the first
// refusal and where it stands — service and key — the way mymo
// names every fix; a file with several problems answers with the
// first and is checked again after the operator acts.
func ParseCompose(text string) (ComposeFile, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return ComposeFile{}, fmt.Errorf("the compose file does not parse: %v", err)
	}
	if len(root.Content) == 0 {
		return ComposeFile{}, fmt.Errorf("the compose file is empty")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return ComposeFile{}, fmt.Errorf("the compose file must be a mapping at its top level")
	}

	top := mapIndex(doc)
	servicesNode, hasServices := top["services"]
	if !hasServices || servicesNode.Kind != yaml.MappingNode {
		return ComposeFile{}, fmt.Errorf("the compose file declares no services — mymo has nothing to deploy")
	}

	cf := ComposeFile{Builds: map[string]bool{}, Images: map[string]string{}}
	for i := 0; i < len(servicesNode.Content); i += 2 {
		name := servicesNode.Content[i].Value
		body := servicesNode.Content[i+1]
		cf.Services = append(cf.Services, name)
		if err := vetService(name, body, &cf); err != nil {
			return ComposeFile{}, err
		}
	}

	// top-level networks: the one network mymo honors as external
	// is its own; anything else is a foreign claim on the box
	if nets, ok := top["networks"]; ok && nets.Kind == yaml.MappingNode {
		for i := 0; i < len(nets.Content); i += 2 {
			name := nets.Content[i].Value
			body := nets.Content[i+1]
			if body.Kind != yaml.MappingNode {
				continue
			}
			fields := mapIndex(body)
			if driver, ok := firstString(fields, "driver"); ok && driver != "bridge" && driver != "default" {
				return ComposeFile{}, fmt.Errorf("network %q asks for the %q driver — mymo does not deploy on networks it cannot reason about", name, driver)
			}
			if ext, ok := firstBool(fields, "external"); ok && ext && name != NetworkName {
				return ComposeFile{}, fmt.Errorf("network %q is external — the only external network mymo joins is %s; remove the reference or let mymo wire the network", name, NetworkName)
			}
		}
	}

	// a volume block is fine — named volumes are the subset's
	// persistence; only binds are vetted, inside the services
	return cf, nil
}

// vetService checks one service body against the model and records
// what mymo needs from it.
func vetService(name string, body *yaml.Node, cf *ComposeFile) error {
	if body.Kind != yaml.MappingNode {
		return fmt.Errorf("service %q must be a mapping", name)
	}
	fields := mapIndex(body)

	if mode, ok := firstString(fields, "network_mode"); ok && mode != "default" && mode != "bridge" {
		return ComposeFile{}.refuse(name, "network_mode", "host networking and custom network modes reach past the sandbox mymo manages")
	}
	for _, key := range []string{"privileged", "pid", "ipc", "cap_add", "devices", "cgroupns_mode", "security_opt"} {
		if node, ok := fields[key]; ok && !isZeroYAML(node) {
			switch key {
			case "privileged":
				return ComposeFile{}.refuse(name, key, "privileged mode reaches past the container's sandbox")
			case "pid", "ipc", "cgroupns_mode":
				return ComposeFile{}.refuse(name, key, "sharing the host's "+key+" namespace is a host-level capability mymo refuses")
			default:
				return ComposeFile{}.refuse(name, key, key+" grants host-level capabilities mymo refuses")
			}
		}
	}
	if node, ok := fields["ports"]; ok && node.Kind != 0 && !isNullNode(node) {
		return ComposeFile{}.refuse(name, "ports", "published ports open doors beside caddy — mymo's model publishes nothing; expose: is the internal way")
	}
	if node, ok := fields["volumes"]; ok && node.Kind == yaml.SequenceNode {
		for _, entry := range node.Content {
			if entry.Kind == yaml.MappingNode {
				// long syntax: {type, source, target}. tmpfs mounts
				// nothing; a bind's source is a host path exactly
				// when it looks like one
				m := mapIndex(entry)
				if typ, ok := firstString(m, "type"); ok && typ == "tmpfs" {
					continue
				}
				src, _ := firstString(m, "source")
				if typ, ok := firstString(m, "type"); ok && typ == "bind" && isHostPath(src) {
					return ComposeFile{}.refuse(name, "volumes", "the bind "+src+" reaches into the host's filesystem — named volumes and relative binds carry an app's data")
				}
				continue
			}
			if isAbsoluteBind(entry.Value) {
				return ComposeFile{}.refuse(name, "volumes", "the bind "+entry.Value+" reaches into the host's filesystem — named volumes and relative binds carry an app's data")
			}
		}
	}

	if node, ok := fields["build"]; ok && !isNullNode(node) {
		cf.Builds[name] = true
	} else if image, ok := firstString(fields, "image"); ok {
		cf.Images[name] = image
	}
	if _, ok := fields["expose"]; ok {
		cf.Warnings = append(cf.Warnings, name+" declares expose: — mymo routes by the port you choose for it, not by what the file exposes")
	}
	return nil
}

// refuse is the named refusal: service, key, and the reason it
// stands between the file and a deploy.
func (ComposeFile) refuse(service, key, reason string) error {
	return fmt.Errorf("service %q sets %s — %s; remove it, or deploy this compose project yourself", service, key, reason)
}

// mapIndex indexes a YAML mapping node's keys. A mapping node's
// content alternates key, value.
func mapIndex(node *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if node == nil || node.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		out[node.Content[i].Value] = node.Content[i+1]
	}
	return out
}

func firstString(fields map[string]*yaml.Node, key string) (string, bool) {
	node, ok := fields[key]
	if !ok || node == nil || node.Kind != yaml.ScalarNode {
		return "", false
	}
	v := strings.TrimSpace(node.Value)
	if v == "" {
		return "", false
	}
	return v, true
}

func firstBool(fields map[string]*yaml.Node, key string) (bool, bool) {
	v, ok := firstString(fields, key)
	if !ok {
		return false, false
	}
	return v == "true" || v == "yes", true
}

func isZeroYAML(node *yaml.Node) bool {
	return node == nil || isNullNode(node) || (node.Kind == yaml.ScalarNode && node.Tag == "!!null")
}

func isNullNode(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || node.Value == "~" || node.Value == "null")
}

// isAbsoluteBind reports whether a volume short-syntax spec binds
// a host path. A named volume is a bare word; a bind carries a
// path before the colon, and that path is the host's exactly when
// it looks absolute.
func isAbsoluteBind(spec string) bool {
	host, _, found := strings.Cut(spec, ":")
	if !found {
		return false // a bare word: a named volume, or an anonymous volume
	}
	return isHostPath(host)
}

// isHostPath reports whether a volume path reaches at the host:
// absolute, home-relative, or escaping upward.
func isHostPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.HasPrefix(p, "..") || filepathIsAbs(p)
}

// filepathIsAbs keeps Windows-style absolute binds out too — a
// compose file that names C:\ anywhere is reaching at a host.
func filepathIsAbs(p string) bool {
	if len(p) < 2 {
		return false
	}
	return p[1] == ':' || p[0] == '\\'
}
