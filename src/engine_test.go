package main

import (
	"archive/zip"
	"bytes"
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
