package deploy

import (
	"strings"
	"testing"
)

// the canonical small compose app: two services, one building, one
// from a registry, a named volume between them
const goodCompose = `
services:
  api:
    build: .
    environment:
      - DATABASE_URL=postgres://db:5432/app
    depends_on:
      - db
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
      interval: 5s
    volumes:
      - appdata:/data
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: example
    volumes:
      - dbdata:/var/lib/postgresql/data

volumes:
  appdata:
  dbdata:
`

func TestParseComposeAcceptsTheSubset(t *testing.T) {
	cf, err := ParseCompose(goodCompose)
	if err != nil {
		t.Fatalf("a subset compose file should parse: %v", err)
	}
	if len(cf.Services) != 2 || cf.Services[0] != "api" || cf.Services[1] != "db" {
		t.Errorf("services = %v, want [api db]", cf.Services)
	}
	if !cf.Builds["api"] || cf.Builds["db"] {
		t.Errorf("build services = %v, want api only", cf.Builds)
	}
	if cf.Images["db"] != "postgres:16" {
		t.Errorf("db image = %q, want postgres:16", cf.Images["db"])
	}
}

// refusals are the model, not taste — each names its service, its
// key, and its reason
func TestParseComposeRefusesTheModelBreakers(t *testing.T) {
	cases := []struct {
		name  string
		added string
		want  string
	}{
		{"published ports", `
services:
  web:
    image: nginx
    ports:
      - "8080:80"`, `service "web" sets ports`},
		{"privileged", `
services:
  web:
    image: nginx
    privileged: true`, `service "web" sets privileged`},
		{"host network mode", `
services:
  web:
    image: nginx
    network_mode: host`, `service "web" sets network_mode`},
		{"host pid", `
services:
  web:
    image: nginx
    pid: host`, `service "web" sets pid`},
		{"added capabilities", `
services:
  web:
    image: nginx
    cap_add:
      - NET_ADMIN`, `service "web" sets cap_add`},
		{"devices", `
services:
  web:
    image: nginx
    devices:
      - /dev/dri:/dev/dri`, `service "web" sets devices`},
		{"absolute bind", `
services:
  web:
    image: nginx
    volumes:
      - /etc:/host-etc`, `reaches into the host's filesystem`},
		{"foreign external network", `
services:
  web:
    image: nginx
    networks:
      - boxnet
networks:
  boxnet:
    external: true`, `is external`},
		{"custom driver", `
services:
  web:
    image: nginx
networks:
  weird:
    driver: macvlan`, `asks for the "macvlan" driver`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCompose(tc.added)
			if err == nil {
				t.Fatal("the refusal never came")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to name %q", err.Error(), tc.want)
			}
		})
	}
}

// the only external network mymo joins is its own — referencing it
// is how a hand-written file opts into the model, not a refusal
func TestParseComposeAcceptsMymoNetExternal(t *testing.T) {
	cf, err := ParseCompose(`
services:
  web:
    image: nginx
    networks:
      - ` + NetworkName + `
networks:
  ` + NetworkName + `:
    external: true`)
	if err != nil {
		t.Fatalf("referencing mymo's own network must pass: %v", err)
	}
	if len(cf.Services) != 1 || cf.Services[0] != "web" {
		t.Errorf("services = %v", cf.Services)
	}
}

// long-syntax volumes are vetted by their source, and tmpfs is not
// a bind at all
func TestParseComposeVetsLongSyntaxVolumes(t *testing.T) {
	if _, err := ParseCompose(`
services:
  web:
    image: nginx
    volumes:
      - type: tmpfs
        target: /scratch
      - type: volume
        source: dbdata
        target: /var/lib/db
`); err != nil {
		t.Errorf("tmpfs and named volumes must pass: %v", err)
	}
	_, err := ParseCompose(`
services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: /etc
        target: /host-etc
`)
	if err == nil || !strings.Contains(err.Error(), "host's filesystem") {
		t.Errorf("long-syntax bind should refuse with the reason: %v", err)
	}
}

// a release's identity is the configuration itself — the same
// file is the same release, one changed character is a new one
func TestConfigDigestIdentifiesReleases(t *testing.T) {
	a := ConfigDigest(goodCompose)
	if b := ConfigDigest(goodCompose); a != b {
		t.Error("identical configs must share a digest")
	}
	if b := ConfigDigest(goodCompose + "\n# a note\n"); a == b {
		t.Error("a changed config must be a new release")
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("digest = %q, want the sha256 form", a)
	}
}

// a file with no services answers with the honest refusal, and
// broken yaml names the parse, not a stack trace
func TestParseComposeRefusesEmptyAndBroken(t *testing.T) {
	if _, err := ParseCompose(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty file: %v", err)
	}
	if _, err := ParseCompose("services:\n  - just\n  - a\n  - list\n"); err == nil {
		t.Error("services as a list must refuse")
	}
	// a top-level scalar is refused as the mapping problem it is
	if _, err := ParseCompose("just one scalar"); err == nil || !strings.Contains(err.Error(), "must be a mapping") {
		t.Errorf("a scalar top level must refuse as a mapping: %v", err)
	}
	if _, err := ParseCompose("a: [unclosed"); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("broken yaml: %v", err)
	}
}
