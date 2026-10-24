package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- login limit

// loginLimiter caps failed logins per client and overall. The one-second
// sleep of a failed login slows one attacker; parallel ones need a budget.
type loginLimiter struct {
	mu     sync.Mutex
	perIP  map[string]*bucket
	global bucket
}

// bucket is a token bucket refilled continuously: cap tokens, rate per second.
type bucket struct {
	tokens float64
	last   time.Time
}

const (
	loginPerClient = 5.0  // failures a client may burn at once…
	loginClientRef = 12.0 // …then one more every 12 s
	loginGlobal    = 30.0 // failures the whole hub tolerates at once…
	loginGlobalRef = 2.0  // …then one more every 2 s
)

func (b *bucket) level(now time.Time, capacity, every float64) float64 {
	if b.last.IsZero() {
		b.tokens, b.last = capacity, now
	}
	b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()/every)
	b.last = now
	return b.tokens
}

// wait is how long ip must wait before trying again, 0 if it may try now.
func (l *loginLimiter) wait(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP == nil {
		l.perIP = map[string]*bucket{}
	}
	b := l.perIP[ip]
	if b == nil {
		b = &bucket{}
		l.perIP[ip] = b
	}
	if c := b.level(now, loginPerClient, loginClientRef); c < 1 {
		return time.Duration((1 - c) * loginClientRef * float64(time.Second))
	}
	if g := l.global.level(now, loginGlobal, loginGlobalRef); g < 1 {
		return time.Duration((1 - g) * loginGlobalRef * float64(time.Second))
	}
	return 0
}

// failed spends one token from ip's bucket and the global one.
func (l *loginLimiter) failed(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.perIP[ip]; b != nil {
		b.level(now, loginPerClient, loginClientRef)
		b.tokens--
	}
	l.global.level(now, loginGlobal, loginGlobalRef)
	l.global.tokens--
	if len(l.perIP) > 4096 { // forget clients whose bucket is full again
		for k, b := range l.perIP {
			if b.level(now, loginPerClient, loginClientRef) >= loginPerClient {
				delete(l.perIP, k)
			}
		}
	}
}

// clientIP is the client's address. Behind the reverse proxy on this machine
// (the documented setup), that's the first X-Forwarded-For hop, which Caddy
// sets itself; a direct client is its socket's address.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			if first = strings.TrimSpace(first); net.ParseIP(first) != nil {
				return first
			}
		}
	}
	return host
}

// ---------------------------------------------------------------- audit trail

// audited wraps a request to an Engine (ours or a spoke's) and, once it has
// succeeded, journals what the operator did. Never what was typed: only its
// length. Terminal keystrokes go over the WebSocket and are not journaled.
func (h *Hub) audited(w http.ResponseWriter, r *http.Request, machine, path string, serve func(http.ResponseWriter, *http.Request)) {
	ev, ok := describe(r, path)
	if !ok {
		serve(w, r)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	serve(rec, r)
	if rec.status >= 400 {
		return
	}
	ev.T, ev.Machine = time.Now().Unix(), machine
	ev.By = "api"
	if h.cookieAuthed(r) {
		ev.By = "web"
	}
	h.record(ev)
}

// describe turns an Engine API call into a journal event, if it changes
// anything. Small JSON bodies are read and put back for the Engine.
func describe(r *http.Request, path string) (Event, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/") // api labs <id> <verb>…
	if len(parts) < 2 || parts[0] != "api" || parts[1] != "labs" {
		return Event{}, false
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 2:
		var in struct{ Name, Dir, Cmd string }
		peekJSON(r, &in)
		name := in.Name
		if name == "" {
			name = lastElem(in.Dir)
		}
		text := in.Dir
		if in.Cmd != "" {
			text += " $ " + in.Cmd
		}
		return Event{ID: orDash(spokeName(name)), Name: name, Act: "create", Text: text}, true
	case r.Method == http.MethodDelete && len(parts) == 3:
		return Event{ID: parts[2], Name: parts[2], Act: "delete"}, true
	case r.Method != http.MethodPost || len(parts) < 4:
		return Event{}, false
	}
	id, verb := parts[2], strings.Join(parts[3:], "/")
	ev := Event{ID: id, Name: id}
	switch verb {
	case "start", "stop":
		ev.Act = verb
	case "keys":
		var in struct {
			Text  string
			Key   string
			Enter bool
		}
		peekJSON(r, &in)
		ev.Act = "keys"
		var bits []string
		if in.Text != "" {
			bits = append(bits, T("journal.typed", "n", strconv.Itoa(len([]rune(in.Text)))))
		}
		if in.Key != "" {
			bits = append(bits, in.Key)
		}
		if in.Enter {
			bits = append(bits, "Enter")
		}
		ev.Text = strings.Join(bits, " + ")
	case "sh/upload":
		ev.Act = "upload"
		ev.Text = r.URL.Query().Get("dir")
		if r.ContentLength > 0 {
			ev.Text += " (" + strconv.FormatInt(r.ContentLength, 10) + " B)"
		}
	default:
		return Event{}, false
	}
	return ev, true
}

func peekJSON(r *http.Request, v any) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), r.Body))
	if err == nil {
		json.Unmarshal(b, v)
	}
}

func lastElem(p string) string {
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// statusRecorder remembers the status an Engine answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}
