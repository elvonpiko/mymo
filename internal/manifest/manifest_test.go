package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAcceptsTheShapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Manifest
	}{
		{
			name: "image app, public domain",
			text: "name: api\ntype: image\nimage: ghcr.io/x/api:1.2.3\nport: 8080\ndomain: api.example.com\nhealth: /healthz\n",
			want: Manifest{Name: "api", Type: "image", Image: "ghcr.io/x/api:1.2.3", Port: 8080, Domain: "api.example.com", Health: "/healthz"},
		},
		{
			name: "dockerfile app, internal only",
			text: "# a comment line\ntype: dockerfile\nname: worker\nport: 3000\n",
			want: Manifest{Name: "worker", Type: "dockerfile", Dockerfile: ".", Port: 3000},
		},
		{
			name: "quoted values and blank lines",
			text: "\nname: \"api\"\ntype: 'image'\nimage: img:2\n\nport: 80\n",
			want: Manifest{Name: "api", Type: "image", Image: "img:2", Port: 80},
		},
		{
			name: "dockerfile context can be a subdir",
			text: "name: web\ntype: dockerfile\ndockerfile: ./app\nport: 80\n",
			want: Manifest{Name: "web", Type: "dockerfile", Dockerfile: "./app", Port: 80},
		},
	}
	for _, tc := range cases {
		got, err := Parse(tc.text)
		if err != nil {
			t.Errorf("%s: Parse: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: parsed %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestParseRefusesWhatItCannotMean(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"unknown key", "name: a\ntype: image\nimage: i\nport: 80\nreplicas: 3\n", "unknown key \"replicas\""},
		{"duplicate key", "name: a\nname: b\ntype: image\nport: 80\n", "appears twice"},
		{"nesting by spaces", "name: a\ntype: image\nport: 80\n  image: i\n", "nesting"},
		{"nesting by tabs", "name: a\ntype: image\nport: 80\n\timage: i\n", "nesting"},
		{"missing name", "type: image\nimage: i\nport: 80\n", "name is required"},
		{"missing type", "name: a\nport: 80\n", "type is required"},
		{"missing port", "name: a\ntype: image\nimage: i\n", "port is required"},
		{"image type without image", "name: a\ntype: image\nport: 80\n", "requires an image"},
		{"unsupported type", "name: a\ntype: compose\nport: 80\n", "unsupported"},
		{"port not a number", "name: a\ntype: image\nimage: i\nport: http\n", "not a number"},
		{"a line without colon", "name: a\ntype: image\nport\n", "expected key: value"},
	}
	for _, tc := range cases {
		_, err := Parse(tc.text)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to say %q", tc.name, err, tc.want)
		}
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()

	// nothing supported: refused with the modes
	_, draft, _, err := Discover(dir)
	if err == nil || !strings.Contains(err.Error(), "no supported deployment source") {
		t.Errorf("empty dir: err = %v", err)
	}
	if draft != "" {
		t.Error("empty dir produced a draft")
	}

	// a Dockerfile alone: a draft to finish, never a guess deployed
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, draft, found, err := Discover(dir)
	if err != nil {
		t.Fatalf("Dockerfile dir: %v", err)
	}
	if m != (Manifest{}) || found != "Dockerfile" {
		t.Errorf("draft mode returned a manifest: %+v found=%q", m, found)
	}
	if !strings.Contains(draft, "name: ") || !strings.Contains(draft, "port: 8080") {
		t.Errorf("draft incomplete:\n%s", draft)
	}

	// the manifest wins when present
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name: api\ntype: dockerfile\nport: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, draft, found, err = Discover(dir)
	if err != nil {
		t.Fatalf("manifest dir: %v", err)
	}
	if found != FileName || draft != "" || m.Name != "api" || m.Port != 8080 {
		t.Errorf("manifest mode: m=%+v draft=%q found=%q", m, draft, found)
	}

	// a broken manifest is refused, not healed
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name: api\ntype: image\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = Discover(dir)
	if err == nil || !strings.Contains(err.Error(), "port is required") {
		t.Errorf("broken manifest: err = %v", err)
	}
}
