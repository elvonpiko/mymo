package domain

import (
	"fmt"
	"strings"
	"time"
)

// AppSource is where a release's artifact comes from. The source is
// what the operator provides; the artifact is the image that runs:
// an existing OCI image, or a Dockerfile mymo builds on the node.
// A git repository is a source for a later version.
type AppSource string

const (
	// SourceImage deploys an existing image from a registry.
	SourceImage AppSource = "image"
	// SourceDockerfile builds a local Dockerfile on the target node.
	SourceDockerfile AppSource = "dockerfile"
	// SourceCompose runs a vetted compose file on the target node:
	// many services, one release, one app record.
	SourceCompose AppSource = "compose"
)

// ReleaseState is a release's standing. A release is immutable
// deployment state: once recorded it never changes identity, only
// its standing moves — active runs, retired was running and is kept
// for rollback, failed never reached activation.
type ReleaseState string

const (
	ReleaseActive  ReleaseState = "active"
	ReleaseRetired ReleaseState = "retired"
	ReleaseFailed  ReleaseState = "failed"
	// ReleaseGone is the box's own answer, accepted: the release's
	// containers are not running there — a docker rm, a project
	// down, a rebuilt box. The record follows the box: this state
	// is what the box said when asked, and the release keeps its
	// identity for history. A gone release is not a rollback
	// target — its container claim is false; a redeploy is the
	// honest recovery.
	ReleaseGone ReleaseState = "gone"
)

// Release is one immutable deployment record: what ran, from what
// artifact, when, and whether it is the release Caddy is serving.
// The digest — never a mutable tag — is the identity mymo rolls
// back to.
type Release struct {
	ID         int          `json:"id"`
	Digest     string       `json:"digest,omitempty"`
	Source     string       `json:"source,omitempty"`
	Container  string       `json:"container,omitempty"`
	Health     string       `json:"health,omitempty"`
	State      ReleaseState `json:"state"`
	DeployedAt time.Time    `json:"deployed_at,omitempty"`
}

// App is an application mymo manages on one node: what it is, how
// it is exposed, and every release it has had. A single-container
// app runs one container per release; a compose app runs a vetted
// compose file — many services, one release identity, the file's
// digest.
//
// Exposure follows the networking model: the container joins mymo's
// docker network under the app's name and is reached as
// <app>:<port> by Caddy and by every other app — never by a
// published port. An empty Domain means internal-only: reachable
// inside the network, invisible from the internet.
type App struct {
	Name       string    `json:"name"`
	Node       string    `json:"node"`
	Type       AppSource `json:"type"`
	Image      string    `json:"image,omitempty"`      // type image: the registry reference
	Dockerfile string    `json:"dockerfile,omitempty"` // type dockerfile: the build context, "" = .
	Compose    string    `json:"compose,omitempty"`    // type compose: the file's content — the release's configuration
	Service    string    `json:"service,omitempty"`    // type compose: the one exposed service caddy routes to
	Port       int       `json:"port"`                 // the container's internal port
	Domain     string    `json:"domain,omitempty"`     // "" = internal only
	Health     string    `json:"health,omitempty"`     // path, "" = none declared
	// NetworkIP is the app's stable address on mymo's docker
	// network: assigned at the first deploy, pinned by every later
	// one. The public route points here — the one address a host's
	// caddy can reach and a container restart cannot change.
	NetworkIP string    `json:"network_ip,omitempty"` // "" = not assigned yet
	AddedAt   time.Time `json:"added_at,omitempty"`

	// Releases are newest last. The previous working release is
	// kept available; rollback selects it by its ID.
	Releases []Release `json:"releases,omitempty"`
}

// AppOps is the box's command set for one release's verbs: status,
// logs, restart, shell. The commands differ by source — a single
// container is addressed by its name, a compose application by its
// project and service — but the verbs never differ: the operator
// asks the same things of every app.
type AppOps struct {
	Status       []string // prints StatusAnswer when the release runs
	StatusAnswer string   // the line the status command must carry
	Logs         []string
	Restart      []string
	Shell        []string
}

// Ops names the command set for one release. A compose release's
// container record is its compose project; the service the record
// names is the one the verbs address.
func (a App) Ops(rel Release) AppOps {
	if a.Type == SourceCompose && rel.Container != "" {
		proj := rel.Container
		argv := func(args ...string) []string {
			return append([]string{"docker", "compose", "-p", proj}, args...)
		}
		return AppOps{
			Status:       argv("ps", "--status", "running", "--format", "{{.Service}}", a.Service),
			StatusAnswer: a.Service,
			Logs:         argv("logs", "--tail", "100", a.Service),
			Restart:      argv("restart", a.Service),
			Shell:        argv("exec", a.Service, "sh"),
		}
	}
	return AppOps{
		Status:       []string{"docker", "ps", "--filter", "name=" + rel.Container, "--filter", "status=running", "--format", "{{.Names}}"},
		StatusAnswer: rel.Container,
		Logs:         []string{"docker", "logs", "--tail", "100", rel.Container},
		Restart:      []string{"docker", "restart", rel.Container},
		Shell:        []string{"docker", "exec", "-it", rel.Container, "sh"},
	}
}

// ValidateAppName applies the same rules as node names: one word,
// letters/digits/dashes, not the empty word.
func ValidateAppName(name string) error {
	return ValidateNodeName(name)
}

// Validate checks the app against the model's rules. The node must
// exist in the store — the caller checks that; here the app must be
// self-consistent.
func (a App) Validate() error {
	if err := ValidateAppName(a.Name); err != nil {
		return fmt.Errorf("app name: %w", err)
	}
	if strings.TrimSpace(a.Node) == "" {
		return fmt.Errorf("app %q has no node", a.Name)
	}
	switch a.Type {
	case SourceImage:
		if strings.TrimSpace(a.Image) == "" {
			return fmt.Errorf("app %q is type image but declares no image", a.Name)
		}
	case SourceDockerfile:
		// context defaults to the manifest's own directory
	case SourceCompose:
		if strings.TrimSpace(a.Compose) == "" {
			return fmt.Errorf("app %q is type compose but carries no compose file", a.Name)
		}
		if strings.TrimSpace(a.Service) == "" {
			return fmt.Errorf("app %q is type compose but names no service — the one caddy routes to; mymo never guesses it", a.Name)
		}
	default:
		return fmt.Errorf("app %q has unsupported type %q (supported: image, dockerfile, compose)", a.Name, a.Type)
	}
	if a.Port < 1 || a.Port > 65535 {
		return fmt.Errorf("app %q port %d is out of range", a.Name, a.Port)
	}
	if a.Domain != "" {
		d := strings.ToLower(strings.TrimSpace(a.Domain))
		if d != a.Domain || strings.Contains(a.Domain, "/") || strings.Contains(a.Domain, " ") {
			return fmt.Errorf("app %q domain %q is not a bare hostname (lowercase, no scheme, no path)", a.Name, a.Domain)
		}
	}
	if a.Health != "" && !strings.HasPrefix(a.Health, "/") {
		return fmt.Errorf("app %q health path %q must start with /", a.Name, a.Health)
	}
	return nil
}

// ActiveRelease returns the release Caddy is currently serving, when
// there is one.
func (a App) ActiveRelease() (Release, bool) {
	for i := len(a.Releases) - 1; i >= 0; i-- {
		if a.Releases[i].State == ReleaseActive {
			return a.Releases[i], true
		}
	}
	return Release{}, false
}

// RollbackTarget is the release a rollback would select: the most
// recent release that was retired healthy. Failed releases are never
// destinations.
// CurrentRelease is the release the verbs address: the newest one
// that is active, or gone. Gone keeps its claim on the verbs so
// the box can be asked again — and so a container started back by
// hand can be seen running again, and the record follow it.
func (a App) CurrentRelease() (Release, bool) {
	for i := len(a.Releases) - 1; i >= 0; i-- {
		if a.Releases[i].State == ReleaseActive || a.Releases[i].State == ReleaseGone {
			return a.Releases[i], true
		}
	}
	return Release{}, false
}

// RollbackTarget selects the most recent retired release. A gone
// release is never a target: the box said its containers are not
// there, and a rollback that cannot start is a false promise.
func (a App) RollbackTarget() (Release, bool) {
	for i := len(a.Releases) - 1; i >= 0; i-- {
		if a.Releases[i].State == ReleaseRetired {
			return a.Releases[i], true
		}
	}
	return Release{}, false
}

// NextReleaseID hands out the next release number, starting at 1.
// Release IDs are per app and never reused.
func (a App) NextReleaseID() int {
	max := 0
	for _, r := range a.Releases {
		if r.ID > max {
			max = r.ID
		}
	}
	return max + 1
}

// ContainerName is the release's container name on the node.
func ContainerName(app string, id int) string {
	return fmt.Sprintf("mymo-%s-r%d", app, id)
}
