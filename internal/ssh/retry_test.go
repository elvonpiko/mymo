package ssh

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/elvonpiko/mymo/internal/domain"
	"github.com/elvonpiko/mymo/internal/sshtest"
)

// closedPort finds a local port that answers connection refused.
func closedPort(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func fastRetries(t *testing.T) {
	t.Helper()
	origAttempts, origPause := dialAttempts, dialAttemptPause
	t.Cleanup(func() { dialAttempts, dialAttemptPause = origAttempts, origPause })
	dialAttempts = 3
	dialAttemptPause = 5 * time.Millisecond
}

func TestDialRetriesConnectFailuresAndReportsProgress(t *testing.T) {
	fastRetries(t)
	host, port := closedPort(t)

	var lines []string
	client := New(domain.Node{Name: "x", Host: host, Port: port, User: "root", Auth: domain.AuthAgent}, "")
	client.WithProgress(func(line string) { lines = append(lines, line) })

	err := client.Dial(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if want := "tried 3 times"; !errors.Is(err, ErrUnreachable) || !contains(err.Error(), want) {
		t.Fatalf("the report must say how many tries happened: %v", err)
	}
	var sawTry2, sawTry3 bool
	for _, l := range lines {
		if contains(l, "try 2 of 3") {
			sawTry2 = true
		}
		if contains(l, "try 3 of 3") {
			sawTry3 = true
		}
	}
	if !sawTry2 || !sawTry3 {
		t.Fatalf("progress must narrate the retries, saw %v", lines)
	}
	if len(lines) != 2 {
		t.Fatalf("one line per retry, want 2, got %v", lines)
	}
}

func TestDialStopsWhenTheCallerCancelsMidRetry(t *testing.T) {
	origAttempts, origPause := dialAttempts, dialAttemptPause
	t.Cleanup(func() { dialAttempts, dialAttemptPause = origAttempts, origPause })
	dialAttempts = 3
	// the pause outlives the caller's deadline: the retry loop must
	// end with the caller's reason, not grind on to attempt 3
	dialAttemptPause = time.Second
	host, port := closedPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client := New(domain.Node{Name: "x", Host: host, Port: port, User: "root", Auth: domain.AuthAgent}, "")
	start := time.Now()
	err := client.Dial(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("the cancel was not honored mid-retry: took %v", elapsed)
	}
}

func TestDialNeverRetriesAnAuthRefusal(t *testing.T) {
	origAttempts, origPause := dialAttempts, dialAttemptPause
	t.Cleanup(func() { dialAttempts, dialAttemptPause = origAttempts, origPause })
	dialAttempts = 3
	// if the refusal were retried, two pauses would cost at least this
	dialAttemptPause = 300 * time.Millisecond

	keyPath, _ := sshtest.NewKey(t)
	srv := newRejectingServer(t)
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := New(domain.Node{Name: "x", Host: host, Port: port, User: "root",
		Auth: domain.AuthKey, KeyPath: keyPath}, "")

	start := time.Now()
	err := client.Dial(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if elapsed := time.Since(start); elapsed > dialAttemptPause {
		t.Fatalf("an auth refusal was retried: took %v", elapsed)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
