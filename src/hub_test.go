package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTmux puts a stand-in tmux first on PATH: queries fail (no session, no
// server), commands succeed. Enough for the Engine to create and drive labs
// without a tmux server, in CI as anywhere.
func fakeTmux(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n*has-session*|*display-message*|*show-environment*|*list-panes*|*capture-pane*) exit 1 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const operator = "operator-token-0123456789abcdef"

type testHub struct {
	h    *Hub
	srv  *httptest.Server
	data string
}

func startHub(t *testing.T) *testHub {
	t.Helper()
	fakeTmux(t)
	t.Setenv("POSTERM_NAME", "hub")
	data := t.TempDir()
	engine, err := NewEngine(data)
	if err != nil {
		t.Fatal(err)
	}
	h, handler := newHub(engine, data, Secrets{Token: operator, JoinToken: "join-token-0123456789abcdef", VAPIDPublic: "p", VAPIDPrivate: "q"})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &testHub{h: h, srv: srv, data: data}
}

func (th *testHub) do(t *testing.T, method, path, bearer, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, th.srv.URL+path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// spoke joins the test hub as name with token, serving a fresh Engine.
func (th *testHub) spoke(t *testing.T, name, token string) (context.CancelFunc, chan int) {
	t.Helper()
	engine, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(th.srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		code, _ := serveSpoke(ctx, u, name, token, engine.Handler())
		done <- code
	}()
	t.Cleanup(cancel)
	return cancel, done
}

// refused waits for a join to be turned down. A join that gets in instead
// would serve forever: fail rather than hang.
func refused(t *testing.T, done chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(3 * time.Second):
		t.Fatal("the join was accepted (still connected after 3 s)")
		return 0
	}
}

func waitOnline(t *testing.T, h *Hub, name string, want bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		h.mu.Lock()
		m := h.machines[name]
		on := m != nil && m.online && !m.sess.IsClosed()
		h.mu.Unlock()
		if on == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("machine %s online=%v never happened", name, want)
}

func TestTunnelPerMachineToken(t *testing.T) {
	th := startHub(t)
	saveSpokes(th.data, map[string]spokeEntry{"box": {Hash: hashToken("box.secret-0123456789"), Created: 1}})

	// The right token for the right name: joined, and routed through the tunnel.
	th.spoke(t, "box", "box.secret-0123456789")
	waitOnline(t, th.h, "box", true)
	res, body := th.do(t, "GET", "/m/box/api/labs", operator, "")
	if res.StatusCode != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("/m/box/api/labs through the tunnel: %d %q", res.StatusCode, body)
	}
	// The spoke's token opens nothing but /join.
	if res, _ := th.do(t, "GET", "/m/box/api/labs", "box.secret-0123456789", ""); res.StatusCode != 401 {
		t.Errorf("a spoke token on the API: %d, want 401", res.StatusCode)
	}

	// Another name with box's token: refused.
	_, done := th.spoke(t, "other", "box.secret-0123456789")
	if code := refused(t, done); code != 401 {
		t.Errorf("joining as other with box's token: %d, want 401", code)
	}
	// box with the hub-wide token, now that box has its own: refused.
	_, done = th.spoke(t, "box", "join-token-0123456789abcdef")
	if code := refused(t, done); code != 401 {
		t.Errorf("joining as box with the legacy token: %d, want 401", code)
	}

	// Revoking box cuts its live tunnel.
	saveSpokes(th.data, map[string]spokeEntry{})
	th.h.reapSpokes()
	waitOnline(t, th.h, "box", false)
}

func TestTunnelLegacyTokenStillJoins(t *testing.T) {
	th := startHub(t)
	th.spoke(t, "old", "join-token-0123456789abcdef")
	waitOnline(t, th.h, "old", true)
	// Giving it its own token cuts the legacy tunnel.
	saveSpokes(th.data, map[string]spokeEntry{"old": {Hash: hashToken("old.x-0123456789"), Created: 1}})
	th.h.reapSpokes()
	waitOnline(t, th.h, "old", false)
}

func TestAuditTrail(t *testing.T) {
	th := startHub(t)
	dir := t.TempDir()
	res, body := th.do(t, "POST", "/m/hub/api/labs", operator, `{"name":"demo","dir":"`+dir+`","cmd":"claude"}`)
	if res.StatusCode != 200 {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	th.do(t, "POST", "/m/hub/api/labs/demo/keys", operator, `{"text":"hunter2 is my password","enter":true}`)
	th.do(t, "POST", "/m/hub/api/labs/nope/stop", operator, "") // fails: not journaled

	_, journal := th.do(t, "GET", "/api/journal", operator, "")
	var events []Event
	json.Unmarshal([]byte(journal), &events)
	var acts []string
	for _, ev := range events {
		acts = append(acts, ev.Act+":"+ev.ID+":"+ev.Text+":"+ev.By)
	}
	want := []string{"create:demo:" + dir + " $ claude:api", "keys:demo:22 chars + Enter:api"}
	if strings.Join(acts, "|") != strings.Join(want, "|") {
		t.Errorf("journal = %q, want %q", acts, want)
	}
	raw, _ := os.ReadFile(filepath.Join(th.data, "journal.jsonl"))
	if strings.Contains(string(raw)+journal, "hunter2") {
		t.Error("typed text ended up in the journal")
	}
}

func TestLoginRateLimit(t *testing.T) {
	var l loginLimiter
	now := time.Unix(1000, 0)
	for i := 0; i < int(loginPerClient); i++ {
		if w := l.wait("1.2.3.4", now); w != 0 {
			t.Fatalf("attempt %d refused too early", i)
		}
		l.failed("1.2.3.4", now)
	}
	if w := l.wait("1.2.3.4", now); w <= 0 {
		t.Fatal("6th failure in a row must wait")
	}
	if w := l.wait("5.6.7.8", now); w != 0 {
		t.Error("another client must not pay for the first one")
	}
	if w := l.wait("1.2.3.4", now.Add(13*time.Second)); w != 0 {
		t.Error("the bucket must refill")
	}
	// Many clients at once: the global budget runs out.
	var g loginLimiter
	refused := 0
	for i := 0; i < 100; i++ {
		ip := "10.0.0." + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if g.wait(ip, now) != 0 {
			refused++
			continue
		}
		g.failed(ip, now)
	}
	if refused == 0 {
		t.Error("100 clients failing at once must hit the global budget")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if ip := clientIP(r); ip != "203.0.113.7" {
		t.Errorf("behind the local proxy: %q", ip)
	}
	r.RemoteAddr = "198.51.100.2:5555" // not our proxy: its header is not trusted
	if ip := clientIP(r); ip != "198.51.100.2" {
		t.Errorf("direct client: %q", ip)
	}
}

func TestSpokeCommandWritesHashesOnly(t *testing.T) {
	data := t.TempDir()
	spokeCommand(data, []string{"add", "Box 1"})
	spokes := loadSpokes(data)
	e, ok := spokes["box-1"]
	if !ok || len(e.Hash) != 64 {
		t.Fatalf("spokes.json = %+v", spokes)
	}
	raw, _ := os.ReadFile(spokesFile(data))
	if strings.Contains(string(raw), "box-1.") {
		t.Error("spokes.json must hold the hash, never the token")
	}
	st, _ := os.Stat(spokesFile(data))
	if st.Mode().Perm() != 0o644 {
		t.Errorf("spokes.json mode %v", st.Mode().Perm())
	}
	spokeCommand(data, []string{"revoke", "box-1"})
	if len(loadSpokes(data)) != 0 {
		t.Error("revoke left the entry")
	}
}
