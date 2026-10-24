package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The confinement boundary: paths and zips shaped by whoever drives the wall.
// One property everywhere: nothing lands outside the target dir, and a drop
// that fails leaves the dir as it was. CI runs each fuzzer for a short while;
// `go test -fuzz FuzzUnpack ./src` runs one for as long as you like.

func zipBytes(entries map[string]string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range entries {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	return b.Bytes()
}

// assertConfined walks dir and fails on anything that resolves outside it,
// and checks that outside (a sibling sentinel dir) stayed empty.
func assertConfined(t *testing.T, dir, outside string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(dir)
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("a drop created a symlink: %s", p)
		}
		real, err := filepath.EvalSymlinks(p)
		if err == nil && real != root && !strings.HasPrefix(real, root+string(os.PathSeparator)) {
			t.Errorf("%s resolves outside the target: %s", p, real)
		}
		return nil
	})
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was written outside: %v", entries)
	}
}

func entriesOf(dir string) []string {
	var names []string
	filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
		if p != dir {
			names = append(names, p)
		}
		return nil
	})
	return names
}

func FuzzInside(f *testing.F) {
	for _, s := range []string{"a.txt", "sub/b.txt", "..", "../x", "a/../../x", "/etc/passwd", `a\b`, "a\x00b", "./a", "a//b", "a/./b", "…/x"} {
		f.Add(s)
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, rel string) {
		dst, err := inside(dir, rel)
		if err != nil {
			return
		}
		r, rerr := filepath.Rel(dir, dst)
		if rerr != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) || filepath.IsAbs(r) {
			t.Fatalf("inside(%q) accepted %q, which is not under the dir", rel, dst)
		}
	})
}

func FuzzUnpack(f *testing.F) {
	f.Add(zipBytes(map[string]string{"a.txt": "x", "sub/b.txt": "y"}))
	f.Add(zipBytes(map[string]string{"../evil": "x"}))
	f.Add(zipBytes(map[string]string{"/abs": "x", "ok": "y"}))
	f.Add(zipBytes(map[string]string{"proj/a": "x", "proj/b/c": "y"}))
	f.Add(zipBytes(map[string]string{"dir/": "", "dir/f": "z"}))
	f.Add([]byte("PK\x03\x04 not really a zip"))
	f.Fuzz(func(t *testing.T, data []byte) {
		parent := t.TempDir()
		dir, outside := filepath.Join(parent, "drop"), filepath.Join(parent, "outside")
		os.Mkdir(dir, 0o755)
		os.Mkdir(outside, 0o755)
		u := &undo{base: dir}
		_, _, err := unpack(u, bytes.NewReader(data), dir, filepath.Join(dir, "proj"), excl)
		if err != nil {
			u.rollback()
			if left := entriesOf(dir); len(left) != 0 {
				t.Fatalf("a failed unpack left %v behind", left)
			}
		}
		assertConfined(t, dir, outside)
	})
}

func FuzzReceive(f *testing.F) {
	f.Add(`{"files":["a.txt"]}`, "hello")
	f.Add(`{"files":["../a.txt"]}`, "x")
	f.Add(`{"files":["d/e.txt"],"dirs":["empty"]}`, "x")
	f.Add(`{"files":["z.zip"],"unzip":["z.zip"]}`, string(zipBytes(map[string]string{"in/side": "1"})))
	f.Add(`{"files":["z.zip"],"unzip":["z.zip"]}`, string(zipBytes(map[string]string{"../../out": "1"})))
	f.Add(`{"dirs":["/tmp/x"]}`, "")
	f.Fuzz(func(t *testing.T, manifest, content string) {
		parent := t.TempDir()
		dir, outside := filepath.Join(parent, "drop"), filepath.Join(parent, "outside")
		os.Mkdir(dir, 0o755)
		os.Mkdir(outside, 0o755)

		var m struct{ Files []string }
		json.Unmarshal([]byte(manifest), &m)
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		mw, _ := w.CreateFormField("manifest")
		mw.Write([]byte(manifest))
		for _, name := range m.Files {
			fw, _ := w.CreateFormFile("file", name)
			fw.Write([]byte(content))
		}
		w.Close()
		req := httptest.NewRequest("POST", "/upload", &body)
		req.Header.Set("Content-Type", w.FormDataContentType())

		if _, _, err := receive(req, dir, false); err != nil {
			if left := entriesOf(dir); len(left) != 0 {
				t.Fatalf("a failed drop left %v behind", left)
			}
		}
		assertConfined(t, dir, outside)
	})
}
