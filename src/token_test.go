package main

import (
	"bytes"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// A token generated while stderr is a log (systemd, a pipe, this test) must not
// be written there: only where it is kept.
func TestGeneratedTokenStaysOutOfTheLog(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	tok := secret(dir, "join-token", "", "", "log.join_token", "log.join_token_hidden")
	if len(tok) < 16 {
		t.Fatalf("token too short: %q", tok)
	}
	if strings.Contains(buf.String(), tok) {
		t.Fatalf("token leaked into the log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), filepath.Join(dir, "join-token")) {
		t.Fatalf("log should say where the token is kept: %s", buf.String())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "join-token")); err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token not stored: %v %q", err, b)
	}
}

// Under systemd the hub has no HOME and no SHELL: the labs must still get a
// login shell that reads the user's rc files, and set values are kept.
func TestOwnEnvFillsWhatSystemdLeavesOut(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("HOME", "")
	t.Setenv("SHELL", "")
	t.Setenv("USER", "kept")
	ownEnv(me)
	if os.Getenv("HOME") != me.HomeDir {
		t.Errorf("HOME = %q, want %q", os.Getenv("HOME"), me.HomeDir)
	}
	if sh := os.Getenv("SHELL"); sh == "" {
		t.Error("SHELL left empty")
	}
	if os.Getenv("USER") != "kept" {
		t.Errorf("USER overwritten: %q", os.Getenv("USER"))
	}
}
