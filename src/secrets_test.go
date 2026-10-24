package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLabEnvDropsHubConfig(t *testing.T) {
	dotEnvKeys["SOME_API_KEY"] = true
	defer delete(dotEnvKeys, "SOME_API_KEY")
	got := labEnv([]string{
		"PATH=/usr/bin", "HOME=/home/lab", "TMUX=/tmp/x,1,0", "TERM=dumb",
		"POSTERM_TOKEN=secret", "MONIA_TOKEN=secret", "POSTERM_JOIN_TOKEN=secret", "SOME_API_KEY=secret",
	})
	if strings.Join(got, " ") != "PATH=/usr/bin HOME=/home/lab" {
		t.Fatalf("labEnv leaked or dropped too much: %q", got)
	}
}

func TestLoadSecretsMigratesAndHides(t *testing.T) {
	t.Setenv("POSTERM_TOKEN", "")
	t.Setenv("MONIA_TOKEN", "")
	t.Setenv("POSTERM_JOIN_TOKEN", "")
	data := t.TempDir()
	legacy := "0123456789abcdef0123456789abcdef"
	os.WriteFile(filepath.Join(data, "token"), []byte(legacy+"\n"), 0o600)
	old, _ := json.Marshal(map[string]any{"public": "PUB", "private": "PRIV", "origin": "hub.example", "subs": []any{}})
	os.WriteFile(filepath.Join(data, "push.json"), old, 0o600)

	s := loadSecrets(data)
	if s.Token != legacy {
		t.Errorf("token = %q, want the legacy one kept", s.Token)
	}
	if s.VAPIDPublic != "PUB" || s.VAPIDPrivate != "PRIV" {
		t.Errorf("VAPID keys not migrated: %+v", s)
	}
	if len(s.JoinToken) < 32 || s.JoinToken == s.Token {
		t.Errorf("join token = %q, want a fresh distinct one", s.JoinToken)
	}
	if _, err := os.Stat(filepath.Join(data, "token")); !os.IsNotExist(err) {
		t.Error("legacy token file must be gone from the data dir")
	}
	b, _ := os.ReadFile(filepath.Join(data, "push.json"))
	if strings.Contains(string(b), "PRIV") || !strings.Contains(string(b), "hub.example") {
		t.Errorf("push.json must keep the subscriptions and lose the key pair: %s", b)
	}
	st, _ := os.Stat(secretsDir(data))
	if st.Mode().Perm() != 0o700 {
		t.Errorf("secrets/ mode = %v, want 0700", st.Mode().Perm())
	}
	for _, name := range []string{"token", "join-token", "vapid.json"} {
		st, err := os.Stat(filepath.Join(secretsDir(data), name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("secrets/%s: %v, mode %v", name, err, st.Mode().Perm())
		}
	}

	// A second start reads the same values back.
	again := loadSecrets(data)
	if again != s {
		t.Errorf("secrets changed across restarts: %+v vs %+v", again, s)
	}
}

func TestLoadSecretsRejectsPlantedDir(t *testing.T) {
	t.Setenv("POSTERM_TOKEN", "")
	t.Setenv("MONIA_TOKEN", "")
	data := t.TempDir()
	planted := t.TempDir()
	os.WriteFile(filepath.Join(planted, "token"), []byte("attacker-chosen-token-0123456789\n"), 0o600)
	if err := os.Symlink(planted, secretsDir(data)); err != nil {
		t.Fatal(err)
	}
	s := loadSecrets(data)
	if s.Token == "attacker-chosen-token-0123456789" {
		t.Fatal("a secrets/ symlink planted by a lab was trusted")
	}
	if st, err := os.Lstat(secretsDir(data)); err != nil || !st.IsDir() {
		t.Fatalf("secrets/ must be recreated as a real directory: %v", err)
	}
}

func TestReadSecretRefusesShared(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	os.WriteFile(p, []byte("0123456789abcdef0123\n"), 0o644)
	if _, err := readSecret(p); err == nil {
		t.Error("a group/world readable file must not be read as a secret")
	}
}

func TestJoinTokenOpensJoinOnly(t *testing.T) {
	h := &Hub{token: "operator-token-0123456789", joinToken: "join-token-0123456789"}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	guard := h.guard(ok)
	for _, tc := range []struct {
		path, bearer string
		want         int
	}{
		{"/join", "join-token-0123456789", 200},
		{"/join", "operator-token-0123456789", 200}, // spokes set up before the join token
		{"/join", "", 401},
		{"/join", "nope", 401},
		{"/api/labs", "join-token-0123456789", 401},
		{"/m/x/api/labs", "join-token-0123456789", 401},
		{"/api/labs", "operator-token-0123456789", 200},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.bearer != "" {
			r.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s with %q: %d, want %d", tc.path, tc.bearer, w.Code, tc.want)
		}
	}
}

func TestSecureHub(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://hub.example.org": true,
		"wss://hub.example.org":   true,
		"http://127.0.0.1:8206":   true,
		"http://[::1]:8206":       true,
		"http://localhost:8206":   true,
		"http://hub.example.org":  false,
		"http://10.0.0.2:8206":    false,
		"ws://hub.example.org":    false,
	} {
		u, _ := url.Parse(raw)
		if got := secureHub(u); got != want {
			t.Errorf("secureHub(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestUploadRefusesSymlinkDetour(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	os.Symlink(elsewhere, filepath.Join(dir, "out"))
	os.WriteFile(filepath.Join(elsewhere, "victim"), []byte("x"), 0o600)
	os.Symlink(filepath.Join(elsewhere, "victim"), filepath.Join(dir, "link"))
	u := &undo{base: dir}

	if err := u.mkdir(filepath.Join(dir, "out", "sub")); err == nil {
		t.Error("mkdir through a symlinked dir must be refused")
	}
	if _, err := u.open(filepath.Join(dir, "out", "f"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err == nil {
		t.Error("a file through a symlinked dir must be refused")
	}
	if f, err := u.open(filepath.Join(dir, "link"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		f.Close()
		t.Error("writing through a symlinked file must be refused")
	}
	if b, _ := os.ReadFile(filepath.Join(elsewhere, "victim")); string(b) != "x" {
		t.Error("the file outside was modified")
	}
	if f, err := u.open(filepath.Join(dir, "fine"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err != nil {
		t.Errorf("a plain file inside must still work: %v", err)
	} else {
		f.Close()
	}
}

func TestRegistryLockSerialisesWriters(t *testing.T) {
	e := &Engine{data: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := e.lockLabs()
			defer unlock()
			labs := e.loadLabs()
			e.saveLabs(append(labs, Lab{ID: "x"}))
		}()
	}
	wg.Wait()
	if n := len(e.loadLabs()); n != 20 {
		t.Errorf("%d labs saved, want 20: a read-modify-write was lost", n)
	}
}
