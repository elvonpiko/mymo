package deploy

import (
	"context"

	"github.com/elvonpiko/mymo/internal/domain"
	"strings"
	"testing"
)

// A failed build's note must carry docker's reason, not BuildKit's
// opening ceremony. The header "#0 building with..." is the first
// thing docker prints and the last thing an operator needs; the
// step's output and the ERROR line are the diagnosis. Both shapes
// below came from real builds.
func TestBuildFailNoteFindsTheReason(t *testing.T) {
	dnsOut := `#0 building with "default" instance using docker driver
#1 [internal] load build definition from Dockerfile
#1 DONE 0.0s
#11 [build 3/6] COPY go.mod ./
#11 DONE 0.0s
#12 [build 4/6] RUN go mod download
#12 30.92 go: github.com/go-chi/chi/v5@v5.3.2: Get "https://proxy.golang.org/github.com/go-chi/chi/v5/@v/v5.3.2.mod": dial tcp: lookup proxy.golang.org: i/o timeout
#12 ERROR: process "/bin/sh -c go mod download" did not complete successfully: exit code: 1
------
 > [build 4/6] RUN go mod download:
ERROR: failed to build: failed to solve: process "/bin/sh -c go mod download" did not complete successfully: exit code: 1
`
	note := buildFailNote(dnsOut)
	if !strings.Contains(note, "lookup proxy.golang.org: i/o timeout") {
		t.Errorf("the DNS cause is missing:\n%s", note)
	}
	if !strings.Contains(note, "go mod download") {
		t.Errorf("the failing command is missing:\n%s", note)
	}
	if strings.Contains(note, "#0 building with") {
		t.Errorf("the note opens with BuildKit's header instead of the reason:\n%s", note)
	}

	// the missing go.sum shape: the cause is the compiler's own
	// words, the last lines before the ERROR
	sumOut := `#0 building with "default" instance using docker driver
#13 [build 5/6] RUN go build .
#13 9.4 main.go:18:2: missing go.sum entry for module providing package github.com/go-chi/chi/v5 (imported by example.com/simple-go-web); to add:
#13 9.4 	go get example.com/simple-go-web
#13 ERROR: process "/bin/sh -c go build ." did not complete successfully: exit code: 1
ERROR: failed to build: failed to solve: process "/bin/sh -c go build ." did not complete successfully: exit code: 1
`
	note = buildFailNote(sumOut)
	if !strings.Contains(note, "missing go.sum entry for module providing package github.com/go-chi/chi/v5") {
		t.Errorf("the go.sum cause is missing:\n%s", note)
	}
	if !strings.Contains(note, "go get example.com/simple-go-web") {
		t.Errorf("the remedy line is missing:\n%s", note)
	}

	// a build with an ERROR and no cause lines still names the
	// failing command
	bare := "#5 ERROR: process \"/bin/sh -c make\" did not complete successfully: exit code: 2\n"
	note = buildFailNote(bare)
	if !strings.Contains(note, "make") || strings.Contains(note, "#5 ERROR") {
		t.Errorf("the bare ERROR shape lost the command or kept the prefix:\n%s", note)
	}

	// the classic builder speaks without BuildKit: the old
	// reason-line rules still answer
	if note := buildFailNote("E: unable to fetch the toolchain\n"); !strings.Contains(note, "unable to fetch") {
		t.Errorf("the classic fallback broke:\n%s", note)
	}
}

// Whose fix a failed build is: the note's words carry the layer,
// and the operator gets the sentence that says which side of the
// wire to work on.
func TestOwnTheBuildFailure(t *testing.T) {
	got := OwnTheBuildFailure("dial tcp: lookup proxy.golang.org: i/o timeout — process did not complete")
	if !strings.Contains(got, "the node") || !strings.Contains(got, "docker networking") {
		t.Errorf("a network failure is the box's, not the app's:\n%s", got)
	}
	got = OwnTheBuildFailure("main.go:18:2: missing go.sum entry for module providing package github.com/go-chi/chi/v5")
	if !strings.Contains(got, "inside the project") || !strings.Contains(got, "the project's") {
		t.Errorf("a source failure is the project's, not the node's:\n%s", got)
	}
}

// A failing build through the whole engine: docker's BuildKit
// output goes in, the report's note carries the cause out — and
// the ownership sentence reads the layer from it.
func TestBuildFailureNamesTheReasonThroughTheEngine(t *testing.T) {
	app := imageApp()
	app.Type = domain.SourceDockerfile
	app.Image = ""
	app.Dockerfile = "."
	app.Domain = ""
	app.Health = ""

	r := nodeRunner()
	r.codes["docker build -t mymo/api:r1 /tmp/mymo-build/api-r1"] = 1
	r.responses["docker build -t mymo/api:r1 /tmp/mymo-build/api-r1"] = `#0 building with "default" instance using docker driver
#13 [build 5/6] RUN go build .
#13 9.4 main.go:18:2: missing go.sum entry for module providing package github.com/go-chi/chi/v5 (imported by example.com/simple-go-web); to add:
#13 9.4 	go get example.com/simple-go-web
#13 ERROR: process "/bin/sh -c go build ." did not complete successfully: exit code: 1
ERROR: failed to build: failed to solve: process "/bin/sh -c go build ." did not complete successfully: exit code: 1
`
	copier := &fakeCopier{}
	opts := goodOpts()
	opts.Copier = copier
	opts.BuildContext = t.TempDir()

	res := Deploy(context.Background(), r, app, opts)
	if res.OK {
		t.Fatal("the deploy should have failed")
	}
	if res.FailedAt != "build" {
		t.Fatalf("the failure landed at %s, want build", res.FailedAt)
	}
	var note string
	for _, st := range res.Steps {
		if st.State == StepFailed {
			note = st.Note
		}
	}
	if !strings.Contains(note, "missing go.sum entry") || !strings.Contains(note, "go get example.com/simple-go-web") {
		t.Errorf("the note lost docker's reason:\n%s", note)
	}
	if strings.Contains(note, "#0 building with") {
		t.Errorf("the note opens with BuildKit's header:\n%s", note)
	}
	if got := OwnTheBuildFailure(note); !strings.Contains(got, "inside the project") {
		t.Errorf("the ownership sentence misread the layer:\n%s", got)
	}
}
