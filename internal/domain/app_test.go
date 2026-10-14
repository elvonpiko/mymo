package domain

import (
	"strings"
	"testing"
	"time"
)

func goodApp() App {
	return App{
		Name: "api", Node: "web-1", Type: SourceImage,
		Image: "ghcr.io/elvonpiko/api:1.2.3", Port: 8080,
		Domain: "api.example.com", Health: "/healthz",
	}
}

func TestAppValidateAcceptsEveryShape(t *testing.T) {
	cases := []App{
		goodApp(),
		func() App { a := goodApp(); a.Type = SourceDockerfile; a.Image = ""; a.Dockerfile = "."; return a }(),
		func() App { a := goodApp(); a.Domain = ""; return a }(), // internal only
		func() App { a := goodApp(); a.Health = ""; return a }(), // no health declared
	}
	for _, a := range cases {
		if err := a.Validate(); err != nil {
			t.Errorf("Validate(%s): %v", a.Name, err)
		}
	}
}

func TestAppValidateRefusesTheWrongShapes(t *testing.T) {
	cases := []struct {
		fix  func(App) App
		want string
	}{
		{func(a App) App { a.Name = "two words"; return a }, "app name"},
		{func(a App) App { a.Node = ""; return a }, "no node"},
		{func(a App) App { a.Image = ""; return a }, "declares no image"},
		{func(a App) App { a.Type = "k8s"; return a }, "unsupported type"},
		{func(a App) App { a.Type = SourceCompose; return a }, "carries no compose file"},
		{func(a App) App { a.Type = SourceCompose; a.Compose = "services:\n  a:\n    image: x"; return a }, "names no service"},
		{func(a App) App { a.Port = 0; return a }, "out of range"},
		{func(a App) App { a.Port = 70000; return a }, "out of range"},
		{func(a App) App { a.Domain = "https://api.example.com"; return a }, "bare hostname"},
		{func(a App) App { a.Domain = "API.example.com"; return a }, "bare hostname"},
		{func(a App) App { a.Health = "healthz"; return a }, "must start with /"},
	}
	for _, tc := range cases {
		if err := tc.fix(goodApp()).Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate error = %v, want it to say %q", err, tc.want)
		}
	}
}

func TestReleaseBookkeeping(t *testing.T) {
	now := time.Now()
	a := App{Name: "api", Node: "web-1", Type: SourceImage, Image: "img:1", Port: 80}
	a.Releases = []Release{
		{ID: 1, State: ReleaseRetired, DeployedAt: now},
		{ID: 2, State: ReleaseFailed, DeployedAt: now},
		{ID: 3, State: ReleaseActive, DeployedAt: now},
	}
	if id := a.NextReleaseID(); id != 4 {
		t.Errorf("NextReleaseID = %d, want 4", id)
	}
	act, ok := a.ActiveRelease()
	if !ok || act.ID != 3 {
		t.Errorf("ActiveRelease = %d ok=%v, want 3", act.ID, ok)
	}
	// rollback never selects a failed release
	rb, ok := a.RollbackTarget()
	if !ok || rb.ID != 1 {
		t.Errorf("RollbackTarget = %d ok=%v, want 1", rb.ID, ok)
	}
	if got := ContainerName("api", 3); got != "mymo-api-r3" {
		t.Errorf("ContainerName = %q", got)
	}

	// an app with only a failed release has nothing to roll back to
	a.Releases = []Release{{ID: 1, State: ReleaseFailed}}
	if _, ok := a.RollbackTarget(); ok {
		t.Error("a failed release is not a rollback target")
	}
}

// the verbs never differ; the commands do — a compose application
// is addressed by its project and service, a single container by
// its name
func TestOpsNamesTheRightCommands(t *testing.T) {
	single := App{Name: "api", Node: "web-1", Type: SourceImage, Image: "img:1", Port: 8080}
	rel := Release{ID: 2, Container: "mymo-api-r2", State: ReleaseActive}
	ops := single.Ops(rel)
	if strings.Join(ops.Status, " ") != "docker ps --filter name=mymo-api-r2 --filter status=running --format {{.Names}}" {
		t.Errorf("single status = %v", ops.Status)
	}
	if ops.StatusAnswer != "mymo-api-r2" || strings.Join(ops.Shell, " ") != "docker exec -it mymo-api-r2 sh" {
		t.Errorf("single ops = %+v", ops)
	}

	comp := App{Name: "web", Node: "web-1", Type: SourceCompose, Service: "api", Port: 8080,
		Compose: "services:\n  api:\n    image: x\n"}
	rel = Release{ID: 1, Container: "mymo-web", State: ReleaseActive}
	ops = comp.Ops(rel)
	if strings.Join(ops.Status, " ") != "docker compose -p mymo-web ps --status running --format {{.Service}} api" {
		t.Errorf("compose status = %v", ops.Status)
	}
	if ops.StatusAnswer != "api" {
		t.Errorf("compose answer = %q", ops.StatusAnswer)
	}
	if strings.Join(ops.Logs, " ") != "docker compose -p mymo-web logs --tail 100 api" {
		t.Errorf("compose logs = %v", ops.Logs)
	}
	if strings.Join(ops.Restart, " ") != "docker compose -p mymo-web restart api" {
		t.Errorf("compose restart = %v", ops.Restart)
	}
	if strings.Join(ops.Shell, " ") != "docker compose -p mymo-web exec api sh" {
		t.Errorf("compose shell = %v", ops.Shell)
	}
}
