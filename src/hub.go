package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/hashicorp/yamux"
)

//go:embed web
var webFS embed.FS

// A machine is a spoke: its Engine handler, reached through a yamux session
// multiplexed over the single outbound WebSocket the spoke opened to us.
type machine struct {
	sess   *yamux.Session
	proxy  *httputil.ReverseProxy
	client *http.Client
	labs   []LabState
	online bool
}

type MachineView struct {
	Name   string     `json:"name"`
	Online bool       `json:"online"`
	Labs   []LabState `json:"labs"`
}

// Event is one line of the append-only journal: a post-it changed colour.
type Event struct {
	T       int64  `json:"t"`
	Machine string `json:"m"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
}

type Hub struct {
	data   string
	name   string
	title  string // POSTERM_TITLE: the one name shown everywhere (page, PWA, notifications)
	token  string
	cookie string
	local  *Engine

	mu       sync.Mutex
	machines map[string]*machine
	prev     map[string]LabState // "machine/id" → last seen state
	world    []byte              // last JSON snapshot sent to browsers
	streams  map[chan []byte]bool
	journal  []Event
	push     pushState

	localHandler http.Handler
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}

func runHub(engine *Engine, data string) {
	h := &Hub{data: data, name: machineName(), title: cfgOr("TITLE", "POSTERM"), local: engine, localHandler: engine.Handler(),
		machines: map[string]*machine{}, prev: map[string]LabState{}, streams: map[chan []byte]bool{}}
	h.token = h.loadToken()
	sum := sha256.Sum256([]byte("monia-cookie:" + h.token)) // pre-rename salt: changing it logs every browser out
	h.cookie = hex.EncodeToString(sum[:])
	h.loadJournal()
	h.loadPush()

	go engine.Run()
	go h.watch()

	r := gin.New()
	r.Use(gin.Recovery())

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	r.NoRoute(func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		files.ServeHTTP(c.Writer, c.Request)
	})
	// The page and the manifest are filled in once, at startup: title, language,
	// and every {{t:key}}. The browser gets the rest of the dictionary as /i18n.js.
	page := titled(static, "index.html", h.title, html.EscapeString)
	manifest := titled(static, "manifest.webmanifest", h.title, func(s string) string {
		quoted, _ := json.Marshal(s)
		return string(quoted[1 : len(quoted)-1])
	})
	words, _ := json.Marshal(dict())
	i18nJS := append(append([]byte("window.I18N = "), words...), ";\n"...)
	r.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(200, "text/html; charset=utf-8", page)
	})
	r.GET("/i18n.js", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(200, "text/javascript; charset=utf-8", i18nJS)
	})
	r.GET("/manifest.webmanifest", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(200, "application/manifest+json", manifest)
	})

	r.POST("/api/login", h.login)
	r.POST("/api/logout", func(c *gin.Context) {
		h.setCookie(c, "", -1)
		c.JSON(200, gin.H{"ok": true})
	})
	r.GET("/api/stream", h.stream)
	r.GET("/api/journal", func(c *gin.Context) {
		h.mu.Lock()
		defer h.mu.Unlock()
		c.JSON(200, h.journal)
	})
	r.GET("/api/push/key", func(c *gin.Context) { c.JSON(200, gin.H{"key": h.push.Public}) })
	r.POST("/api/push/sub", h.subscribe)
	r.POST("/api/push/test", func(c *gin.Context) {
		h.send(gin.H{"title": h.title, "body": T("push.test_body"), "tag": "test", "url": "/"})
		c.JSON(200, gin.H{"ok": true})
	})
	r.GET("/join", h.join)

	// /m/ stays out of gin: each Engine is its own gin and must own the raw
	// ResponseWriter, or its WebSocket upgrade never reaches the wire.
	root := http.NewServeMux()
	root.HandleFunc("/m/", h.route)
	root.Handle("/", r)

	addr := cfgOr("ADDR", "127.0.0.1:8206")
	log.Print(T("log.hub", "name", h.name, "addr", addr, "data", data))
	log.Fatal(http.ListenAndServe(addr, h.guard(root)))
}

// titled reads an embedded web file and fills in {{TITRE}}, {{LANG}} and every
// {{t:key}}, each value passed through esc for the file's syntax.
func titled(static fs.FS, name, title string, esc func(string) string) []byte {
	b, err := fs.ReadFile(static, name)
	if err != nil {
		log.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("{{TITRE}}"), []byte(esc(title)))
	b = bytes.ReplaceAll(b, []byte("{{LANG}}"), []byte(lang()))
	return i18nKey.ReplaceAllFunc(b, func(m []byte) []byte {
		return []byte(esc(T(string(i18nKey.FindSubmatch(m)[1]))))
	})
}

var i18nKey = regexp.MustCompile(`\{\{t:([a-z0-9_.]+)\}\}`)

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(gin.H{"error": msg})
}

// ---------------------------------------------------------------- auth

// loadToken: one operator, one secret. From POSTERM_TOKEN, else generated once.
func (h *Hub) loadToken() string {
	if t := cfg("TOKEN"); t != "" {
		return t
	}
	path := filepath.Join(h.data, "token")
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) >= 16 {
		return string(bytes.TrimSpace(b))
	}
	raw := make([]byte, 24)
	rand.Read(raw)
	t := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	log.Print(T("log.token", "token", t, "path", path))
	return t
}

func same(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (h *Hub) authed(r *http.Request) bool {
	for _, name := range []string{cookieName, oldCookieName} {
		if c, err := r.Cookie(name); err == nil && same(c.Value, h.cookie) {
			return true
		}
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && same(bearer, h.token)
}

// guard lets the static shell and the login through, and nothing else. Any
// request carrying an Origin must come from our own pages (CSRF, WebSocket).
func (h *Hub) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				jsonError(w, 403, T("err.origin"))
				return
			}
		}
		p := r.URL.Path
		private := strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/m/") || p == "/join"
		if private && p != "/api/login" && !h.authed(r) {
			jsonError(w, 401, T("err.token_needed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// The cookie had another name before the rename: still accepted, always
// dropped, so a logout logs out for real and a login moves to the new name.
const cookieName, oldCookieName = "posterm", "monia"

func (h *Hub) setCookie(c *gin.Context, value string, maxAge int) {
	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(c.Writer, &http.Cookie{Name: oldCookieName, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func (h *Hub) login(c *gin.Context) {
	var in struct{ Token string }
	if c.BindJSON(&in) != nil {
		return
	}
	if !same(strings.TrimSpace(in.Token), h.token) {
		time.Sleep(time.Second) // one operator: brute force can wait
		c.JSON(401, gin.H{"error": T("err.token_bad")})
		return
	}
	h.setCookie(c, h.cookie, 365*24*3600)
	c.JSON(200, gin.H{"ok": true})
}

// ---------------------------------------------------------------- machines

// route sends /m/<machine>/… to the right Engine: ours, or a spoke's.
func (h *Hub) route(w http.ResponseWriter, r *http.Request) {
	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/m/"), "/")
	r.URL.Path, r.URL.RawPath = "/"+rest, ""
	if name == h.name {
		h.localHandler.ServeHTTP(w, r)
		return
	}
	h.mu.Lock()
	m := h.machines[name]
	h.mu.Unlock()
	if m == nil || !m.online {
		jsonError(w, 502, T("err.offline"))
		return
	}
	m.proxy.ServeHTTP(w, r)
}

// join is where a spoke lands. The WebSocket becomes a yamux session on which
// *we* are the client: every stream we open is an HTTP request to the spoke.
func (h *Hub) join(c *gin.Context) {
	name := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(c.Query("name")), "-"), "-")
	if name == "" || name == h.name {
		c.JSON(409, gin.H{"error": T("err.machine_name")})
		return
	}
	ws, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(-1)
	sess, err := yamux.Client(websocket.NetConn(context.Background(), ws, websocket.MessageBinary), yamuxConfig())
	if err != nil {
		return
	}
	defer sess.Close()

	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return sess.Open() }}
	target := &url.URL{Scheme: "http", Host: "rayon"}
	m := &machine{sess: sess, online: true, client: &http.Client{Transport: tr, Timeout: 5 * time.Second},
		proxy: &httputil.ReverseProxy{Transport: tr, Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
		}}}

	h.mu.Lock()
	if old := h.machines[name]; old != nil {
		old.sess.Close()
		m.labs = old.labs
	}
	h.machines[name] = m
	h.mu.Unlock()
	log.Print(T("log.spoke_up", "name", name))

	for !sess.IsClosed() {
		if res, err := m.client.Get("http://rayon/api/labs"); err == nil {
			var labs []LabState
			if json.NewDecoder(res.Body).Decode(&labs) == nil {
				h.mu.Lock()
				m.labs = labs
				h.mu.Unlock()
			}
			res.Body.Close()
		}
		time.Sleep(time.Second)
	}
	h.mu.Lock()
	m.online = false
	h.mu.Unlock()
	log.Print(T("log.spoke_down", "name", name))
}

// runSpoke is the other end: dial out, then serve our Engine on the tunnel.
// No port is opened on this machine.
func runSpoke(engine *Engine, hub, token string) {
	go engine.Run()
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" {
		log.Fatal(T("log.bad_hub_url", "url", hub))
	}
	u.Path, u.RawQuery = "/join", "name="+url.QueryEscape(machineName())
	handler := engine.Handler()
	for wait := time.Second; ; {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ws, res, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
		cancel()
		if err != nil {
			if res != nil && (res.StatusCode == 401 || res.StatusCode == 409) {
				log.Fatal(T("log.spoke_refused", "code", strconv.Itoa(res.StatusCode)))
			}
			log.Print(T("log.hub_down", "err", err.Error(), "wait", wait.String()))
			time.Sleep(wait)
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = time.Second
		ws.SetReadLimit(-1)
		sess, err := yamux.Server(websocket.NetConn(context.Background(), ws, websocket.MessageBinary), yamuxConfig())
		if err == nil {
			log.Print(T("log.spoke_linked", "name", machineName(), "host", u.Host))
			http.Serve(sess, handler)
			sess.Close()
		}
		ws.CloseNow()
		log.Print(T("log.tunnel_lost"))
		time.Sleep(wait)
	}
}

// ---------------------------------------------------------------- the world

func (h *Hub) snapshot() []MachineView {
	views := []MachineView{{Name: h.name, Online: true, Labs: h.local.Snapshot()}}
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.machines))
	for name := range h.machines {
		names = append(names, name)
	}
	sort.Strings(names) // stable wall order
	for _, name := range names {
		m := h.machines[name]
		labs := m.labs
		if labs == nil {
			labs = []LabState{}
		}
		views = append(views, MachineView{Name: name, Online: m.online, Labs: labs})
	}
	return views
}

// watch diffs the world every second: transitions feed the journal and the
// push notifications, changes feed the browsers.
func (h *Hub) watch() {
	minBusy := int64(10) // a lab must have worked this long for "posé" to be news
	for ; ; time.Sleep(time.Second) {
		views := h.snapshot()
		questions := 0
		var events []Event
		var buzz []bool // journal always; notify only when nobody is looking at the lab
		seen := map[string]bool{}
		h.mu.Lock()
		for _, v := range views {
			for _, l := range v.Labs {
				key := v.Name + "/" + l.ID
				seen[key] = true
				if l.State == Question && v.Online {
					questions++
				}
				old, known := h.prev[key]
				h.prev[key] = l
				if !known || !v.Online || old.State == l.State {
					continue
				}
				text := l.Note
				if text == "" {
					text = tail(l.Screen, 6)
				}
				switch {
				case l.State == Question,
					l.State == Pose && old.State == EnCours && l.Since-old.Since >= minBusy:
				case l.State == Eteint && old.State != Eteint && l.Code != "":
					text = T("journal.exited", "code", l.Code) + "\n" + text
				default:
					continue
				}
				events = append(events, Event{T: l.Since, Machine: v.Name, ID: l.ID, Name: l.Name,
					From: old.State, To: l.State, Text: text})
				buzz = append(buzz, l.Seen == 0)
			}
		}
		for key := range h.prev {
			if !seen[key] {
				delete(h.prev, key)
			}
		}
		h.mu.Unlock()

		for i, ev := range events {
			h.record(ev)
			if buzz[i] {
				h.send(gin.H{
					"title": T("push."+ev.To, "lab", ev.Name), "body": ev.Text, "n": questions,
					"tag": ev.Machine + "/" + ev.ID, "url": "/#" + ev.Machine + "/" + ev.ID,
					"m": ev.Machine, "id": ev.ID, "reply": ev.To == Question,
				})
			}
		}

		b, _ := json.Marshal(gin.H{"hub": h.name, "machines": views})
		h.mu.Lock()
		if !bytes.Equal(b, h.world) {
			h.world = b
			for ch := range h.streams {
				select { // latest wins: drop the stale frame of a slow browser
				case <-ch:
				default:
				}
				ch <- b
			}
		}
		h.mu.Unlock()
	}
}

// stream is the wall's heartbeat: one SSE event per change of the world.
func (h *Hub) stream(c *gin.Context) {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	h.streams[ch] = true
	if h.world != nil {
		ch <- h.world
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.streams, ch)
		h.mu.Unlock()
	}()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case b := <-ch:
			fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		case <-beat.C:
			fmt.Fprint(c.Writer, ": .\n\n")
		case <-c.Request.Context().Done():
			return
		}
		c.Writer.Flush()
	}
}

// ---------------------------------------------------------------- journal

func (h *Hub) journalFile() string { return filepath.Join(h.data, "journal.jsonl") }

// journalMax is the size past which the journal is rotated at startup into
// journal.1.jsonl (one generation kept): roughly a year of a busy wall.
const journalMax = 4 << 20

func (h *Hub) loadJournal() {
	name := h.journalFile()
	if st, err := os.Stat(name); err == nil && st.Size() > journalMax {
		old := filepath.Join(h.data, "journal.1.jsonl")
		if os.Rename(name, old) == nil {
			name = old // its tail still fills the log panel
		}
	}
	b, _ := os.ReadFile(name)
	for _, line := range bytes.Split(b, []byte("\n")) {
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.ID != "" {
			h.journal = append(h.journal, ev)
		}
	}
	if len(h.journal) > 300 {
		h.journal = h.journal[len(h.journal)-300:]
	}
}

func (h *Hub) record(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.journal = append(h.journal, ev); len(h.journal) > 300 {
		h.journal = h.journal[1:]
	}
	if f, err := os.OpenFile(h.journalFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		line, _ := json.Marshal(ev)
		f.Write(append(line, '\n'))
		f.Close()
	}
}

// ---------------------------------------------------------------- web push

type pushState struct {
	Public  string                 `json:"public"`
	Private string                 `json:"private"`
	Origin  string                 `json:"origin"` // host the operator subscribed from
	Subs    []webpush.Subscription `json:"subs"`
}

func (h *Hub) loadPush() {
	if b, err := os.ReadFile(filepath.Join(h.data, "push.json")); err == nil {
		json.Unmarshal(b, &h.push)
	}
	if h.push.Public == "" {
		var err error
		if h.push.Private, h.push.Public, err = webpush.GenerateVAPIDKeys(); err != nil {
			log.Fatal(err)
		}
		h.savePush()
	}
}

func (h *Hub) savePush() {
	b, _ := json.MarshalIndent(h.push, "", "  ")
	os.WriteFile(filepath.Join(h.data, "push.json"), b, 0o600)
}

func (h *Hub) subscribe(c *gin.Context) {
	var sub webpush.Subscription
	if c.BindJSON(&sub) != nil || sub.Endpoint == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.push.Origin = c.Request.Host
	h.push.Subs = slices.DeleteFunc(h.push.Subs, func(s webpush.Subscription) bool { return s.Endpoint == sub.Endpoint })
	h.push.Subs = append(h.push.Subs, sub)
	h.savePush()
	c.JSON(200, gin.H{"ok": true})
}

// send fans a payload out to every subscribed browser, forgetting dead ones.
func (h *Hub) send(payload gin.H) {
	payload["replyLabel"], payload["replyHint"] = T("push.reply"), T("push.reply_hint") // sw.js has no dictionary
	body, _ := json.Marshal(payload)
	h.mu.Lock()
	st := h.push
	st.Subs = slices.Clone(st.Subs)
	h.mu.Unlock()
	for _, sub := range st.Subs {
		go func() {
			res, err := webpush.SendNotification(body, &sub, &webpush.Options{
				Subscriber: "https://" + st.Origin, VAPIDPublicKey: st.Public, VAPIDPrivateKey: st.Private,
				TTL: 3600, Urgency: webpush.UrgencyHigh})
			if err != nil {
				log.Printf("push : %v", err)
				return
			}
			res.Body.Close()
			if res.StatusCode == 404 || res.StatusCode == 410 {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.push.Subs = slices.DeleteFunc(h.push.Subs, func(s webpush.Subscription) bool { return s.Endpoint == sub.Endpoint })
				h.savePush()
			}
		}()
	}
}
