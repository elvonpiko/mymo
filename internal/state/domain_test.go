package state

import (
	"errors"
	"testing"

	"github.com/elvonpiko/mymo/internal/domain"
)

// A public domain belongs to exactly one app. The conflict is
// detectable before any mutation, so the store refuses it before
// any deploy could race two route files for one hostname.
func TestAPublicDomainBelongsToOneApp(t *testing.T) {
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	web := domain.App{Name: "web", Node: "web-1", Type: domain.SourceImage,
		Image: "nginx:1.27", Port: 8080, Domain: "api.example.com"}
	if err := s.AddApp(web); err != nil {
		t.Fatal(err)
	}

	// a second app cannot claim the same hostname — the error names
	// the owner, so the operator knows where the route lives
	api := domain.App{Name: "api", Node: "web-1", Type: domain.SourceImage,
		Image: "ghcr.io/example/api:2", Port: 3000, Domain: "api.example.com"}
	err = s.AddApp(api)
	if !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("a stolen domain went through: %v", err)
	}
	if got := err.Error(); got != "domain already belongs to another app: api.example.com belongs to web" {
		t.Errorf("the conflict does not name the owner: %q", got)
	}

	// updates cannot steal it either
	other := domain.App{Name: "other", Node: "web-1", Type: domain.SourceImage,
		Image: "redis:7", Port: 6379}
	if err := s.AddApp(other); err != nil {
		t.Fatal(err)
	}
	other.Domain = "api.example.com"
	if err := s.UpdateApp(other); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("an update stole the domain: %v", err)
	}

	// the owner keeps its own domain through updates
	web.Image = "nginx:1.28"
	if err := s.UpdateApp(web); err != nil {
		t.Fatalf("the owner could not keep its own domain: %v", err)
	}

	// internal-only apps never collide — no domain, no claim
	inner := domain.App{Name: "inner", Node: "web-1", Type: domain.SourceImage,
		Image: "redis:7", Port: 6379}
	if err := s.AddApp(inner); err != nil {
		t.Fatal(err)
	}
}
