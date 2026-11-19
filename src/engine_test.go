package main

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInside(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"a.txt", "sub/b.txt", "x..y", ".hidden"} {
		if dst, err := inside(dir, rel); err != nil || dst != filepath.Join(dir, rel) {
			t.Errorf("inside(%q) = %q, %v; want it accepted", rel, dst, err)
		}
	}
	for _, rel := range []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd", `a\b`, "a\x00b", "a/.."} {
		if dst, err := inside(dir, rel); err == nil {
			t.Errorf("inside(%q) = %q; want it refused", rel, dst)
		}
	}
}

// zipOf builds a zip in memory: name → content, a trailing "/" makes a dir.
func zipOf(t *testing.T, entries ...string) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("x"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &b
}

const excl = os.O_WRONLY | os.O_CREATE | os.O_EXCL

func TestUnpack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "proj")
	u := &undo{}
	n, _, err := unpack(u, zipOf(t, "proj/", "proj/a.txt", "proj/s/b.txt", "__MACOSX/proj/._a.txt", "proj/.DS_Store"), dir, target, excl)
	if err != nil || n != 2 {
		t.Fatalf("unpack = %d, %v; want 2 files", n, err)
	}
	for _, p := range []string{"a.txt", "s/b.txt"} { // not proj/proj/…
		if _, err := os.Stat(filepath.Join(target, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	for _, p := range []string{".DS_Store", "__MACOSX"} {
		if _, err := os.Stat(filepath.Join(target, p)); err == nil {
			t.Errorf("%s should have been skipped", p)
		}
	}
	u.rollback()
	if _, err := os.Stat(target); err == nil {
		t.Error("rollback left the target behind")
	}
}

func TestUnpackRefusesEscape(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "evil")
	u := &undo{}
	if _, _, err := unpack(u, zipOf(t, "ok.txt", "../escaped.txt"), dir, target, excl); err == nil {
		t.Fatal("a zip with ../ was accepted")
	}
	// all or nothing: not even the good entry, nor the target dir
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("refused zip still wrote %v", entries)
	}
}

func TestUnpackNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "p")
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "a.txt"), []byte("mine"), 0o644)
	if _, _, err := unpack(&undo{}, zipOf(t, "a.txt"), dir, target, excl); err == nil {
		t.Fatal("overwrote an existing file without O_EXCL being lifted")
	}
	if b, _ := os.ReadFile(filepath.Join(target, "a.txt")); string(b) != "mine" {
		t.Errorf("existing file changed: %q", b)
	}
}

func TestDropAllowed(t *testing.T) {
	// No tmux server on this socket: shellDir fails, only handed-out dirs pass.
	e := &Engine{data: t.TempDir(), drops: map[string]time.Time{}}
	l := Lab{ID: "lab1"}
	if e.dropAllowed(l, "/tmp") {
		t.Fatal("a dir never handed out was allowed")
	}
	e.allowDrop("lab1", "/srv/x")
	if !e.dropAllowed(l, "/srv/x") {
		t.Error("a dir handed out by check was refused")
	}
	if e.dropAllowed(Lab{ID: "lab2"}, "/srv/x") {
		t.Error("a dir handed out for one lab was allowed for another")
	}
	e.drops["lab1\n/srv/x"] = time.Now().Add(-time.Second)
	if e.dropAllowed(l, "/srv/x") {
		t.Error("an expired dir was allowed")
	}
}

func TestCfgFallsBackToPreRenameName(t *testing.T) {
	t.Setenv("POSTERM_ZZTEST", "")
	t.Setenv("MONIA_ZZTEST", "old")
	if got := cfg("ZZTEST"); got != "old" {
		t.Errorf("cfg = %q, want the MONIA_ value", got)
	}
	t.Setenv("POSTERM_ZZTEST", "new")
	if got := cfg("ZZTEST"); got != "new" {
		t.Errorf("cfg = %q, want POSTERM_ to win", got)
	}
	if got := cfgOr("ZZNONE", "def"); got != "def" {
		t.Errorf("cfgOr = %q, want the default", got)
	}
}

func TestShellName(t *testing.T) {
	for _, ok := range []string{"a.txt", "a b.txt", "l'été.md", ".", "..", ".hidden", "-rf"} {
		if !shellName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/b", "x\x00"} {
		if shellName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestIsShell(t *testing.T) {
	for _, cmd := range []string{"bash", "-bash", "zsh", "fish\n"} {
		if !isShell(cmd) {
			t.Errorf("%q is a shell", cmd)
		}
	}
	for _, cmd := range []string{"less", "vim", "tail", "claude", ""} {
		if isShell(cmd) {
			t.Errorf("%q is not a shell", cmd)
		}
	}
}

func TestDescribeDownload(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/labs/lab1/sh/file?name=a+b.txt", nil)
	ev, ok := describe(r, "/api/labs/lab1/sh/file")
	if !ok || ev.Act != "download" || ev.ID != "lab1" || ev.Text != "a b.txt" {
		t.Errorf("describe = %+v, %v", ev, ok)
	}
	if _, ok := describe(httptest.NewRequest("GET", "/api/labs/lab1/sh", nil), "/api/labs/lab1/sh"); ok {
		t.Error("opening the bash got journaled")
	}
}

// scriptTmux puts a tmux on PATH that logs its arguments to the returned file
// and then runs body (sh, "$*" is the command line).
func scriptTmux(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >>" + shq(log) + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestShellStartsWhenTmuxAnswersEmpty(t *testing.T) {
	// tmux 3.5: display-message on a missing target prints "" and exits 0.
	log := scriptTmux(t, `case "$*" in *has-session*) exit 1 ;; *display-message*) echo; exit 0 ;; esac; exit 0`)
	e := &Engine{data: t.TempDir()}
	l := Lab{ID: "lab1", Dir: t.TempDir()}
	if err := e.shell(l); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if !bytes.Contains(calls, []byte("new-session -d -s lab1__sh")) {
		t.Errorf("no new-session for a missing bash:\n%s", calls)
	}
	if _, err := e.shellDir(l); err == nil {
		t.Error("shellDir found a dir for a missing bash")
	}
	if err := e.cd(l, "x"); err == nil || bytes.Contains(calls, []byte("send-keys -t =lab1__sh: -l cd")) {
		t.Errorf("cd typed into a missing bash (err %v)", err)
	}
}

func TestShellKeepsLiveOneRestartsDeadOne(t *testing.T) {
	for dead, restart := range map[string]bool{"0": false, "1": true} {
		log := scriptTmux(t, `case "$*" in *display-message*) echo `+dead+` ;; esac; exit 0`)
		e := &Engine{data: t.TempDir()}
		if err := e.shell(Lab{ID: "lab1", Dir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		calls, _ := os.ReadFile(log)
		if got := bytes.Contains(calls, []byte("new-session")); got != restart {
			t.Errorf("pane_dead=%s: new-session %v, want %v:\n%s", dead, got, restart, calls)
		}
	}
}
