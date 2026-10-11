package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The build context resolution is where a dockerfile deploy decides
// what streams to the node. The draft writes "dockerfile: ." — that
// must mean the project itself, never its parent, and a manifest
// pointing outside the project is refused before anything streams.
func TestResolveBuildContext(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(root, "Dockerfile"), filepath.Join(sub, "Dockerfile")} {
		if err := os.WriteFile(f, []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		dockerfile string
		want       string
	}{
		{".", root},             // the draft's own form: the project itself
		{"Dockerfile", root},    // a file names its directory
		{"./Dockerfile", root},  // the explicit prefix resolves the same
		{"sub/Dockerfile", sub}, // a nested file keeps its subdirectory
		{"sub", sub},            // a directory is the context itself
		{"sub/missing", sub},    // a missing file falls to its directory
		{"no-such-dir/", root},  // an empty name stays the project
	}
	for _, c := range cases {
		got, err := ResolveBuildContext(root, c.dockerfile)
		if err != nil {
			t.Errorf("dockerfile %q: %v", c.dockerfile, err)
			continue
		}
		if got != c.want {
			t.Errorf("dockerfile %q resolved to %s, want %s", c.dockerfile, got, c.want)
		}
	}

	// the parent is never the answer: "." is the project, not what
	// contains it
	parent := filepath.Dir(root)
	if got, err := ResolveBuildContext(root, "."); err != nil || got == parent {
		t.Errorf("dockerfile . escaped to the parent: %s (%v)", got, err)
	}

	// outside the project is refused in plain words
	for _, escape := range []string{"../Dockerfile", "../../x", "sub/../../Dockerfile"} {
		_, err := ResolveBuildContext(root, escape)
		if err == nil || !strings.Contains(err.Error(), "outside the project") {
			t.Errorf("dockerfile %q should be refused: %v", escape, err)
		}
	}

	// no dockerfile named is no build
	if _, err := ResolveBuildContext(root, ""); err == nil || !strings.Contains(err.Error(), "names no dockerfile") {
		t.Errorf("an empty dockerfile should be refused: %v", err)
	}
}
