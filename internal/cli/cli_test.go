package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "mymo ") {
		t.Errorf("output = %q, want a version line", out.String())
	}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var out, errb bytes.Buffer
		if code := Run(context.Background(), []string{arg}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit code = %d, want 0", arg, code)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%s: output missing usage header:\n%s", arg, out.String())
		}
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), nil, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("output missing usage header:\n%s", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"bogus"}, &out, &errb); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errb.String(), `unknown command "bogus"`) {
		t.Errorf("stderr = %q, want unknown-command message", errb.String())
	}
}
