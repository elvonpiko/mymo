package ssh

import "testing"

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"abc123", "abc123"}, // safe as-is
		{"/var/log:syslog%2", "/var/log:syslog%2"}, // safe metacharacters
		{"a b", "'a b'"},                   // space splits words
		{"c$HOME", "'c$HOME'"},             // expansion must never run
		{"a;b", "'a;b'"},                   // command separators
		{"`id`", "'`id`'"},                 // substitution
		{"a'b", `'a'"'"'b'`},               // embedded quote
		{"it's", `'it'"'"'s'`},             // apostrophe
		{`a"b`, `'a"b'`},                   // double quote
		{"line1\nline2", "'line1\nline2'"}, // newline
		{"café", `'café'`},                 // non-ascii
		{"~", "'~'"},                       // tilde is not an expansion
		{"-rf", "-rf"},                     // leading dash is fine in argv
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSerialize(t *testing.T) {
	got := serialize("echo", []string{"a b", "c$HOME"})
	want := `echo 'a b' 'c$HOME'`
	if got != want {
		t.Fatalf("serialize = %q, want %q", got, want)
	}
	if got := serialize("uptime", nil); got != "uptime" {
		t.Fatalf("serialize no-args = %q, want uptime", got)
	}
}
