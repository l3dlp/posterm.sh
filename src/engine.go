package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
)

// A Lab is a "dossier + commande" pair. Its runtime is a tmux session of the
// same name on a dedicated tmux server (socket in the data dir): tmux is the job queue,
// the persistence layer and the multiplexer. The engine only observes it.
type Lab struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Dir  string `json:"dir"`
	Cmd  string `json:"cmd"`
}

// Post-it states.
const (
	EnCours  = "encours"  // rouge : the screen is moving
	Question = "question" // jaune : quiet, and waiting for the human
	Pose     = "pose"     // vert  : quiet, nothing asked
	Eteint   = "eteint"   // gris  : no process
)

type LabState struct {
	Lab
	State  string `json:"state"`
	Since  int64  `json:"since"`          // unix time the state was entered
	Code   string `json:"code,omitempty"` // exit status once the command died
	Seen   int    `json:"seen"`           // attached terminals (web or real)
	Screen string `json:"screen"`         // last lines of the visible screen
	Note   string `json:"note,omitempty"` // text given with `posterm state`

	activity int64  // tmux window_activity
	captured int64  // unix time of the last screen capture
	cursor   string // the line under the cursor at that time
}

// A lab is asking when the line under the cursor looks like a prompt…
var promptRe = regexp.MustCompile(`(?i)(\[y/n\]|\(y/n\)|\[o/n\]|yes/no|oui/non|password|passphrase|mot de passe|press enter|\?$)`)

// …or when a TUI shows a dialog somewhere on screen (overridable: POSTERM_QUESTION).
const defaultDialog = `(?i)(permission required|allow for session|do you want to (proceed|make|create|allow)|❯ *1\. *yes)`

const tmuxConf = `set -g status off
set -g remain-on-exit on
set -g history-limit 20000
set -g mouse on
set -s escape-time 0
set -g focus-events on
set -g default-terminal "screen-256color"
set -ga terminal-overrides ",xterm-256color:Tc"
`

type Engine struct {
	data   string
	quiet  int64
	dialog *regexp.Regexp

	mu     sync.Mutex
	states map[string]*LabState
	order  []string
	drops  map[string]time.Time // lab + "\n" + dir handed out by sh/check → expiry
}

func NewEngine(data string) (*Engine, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, E("err.no_tmux")
	}
	if err := os.MkdirAll(filepath.Join(data, "etat"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(data, "tmux.conf"), []byte(tmuxConf), 0o600); err != nil {
		return nil, err
	}
	re, err := regexp.Compile(cfgOr("QUESTION", defaultDialog))
	if err != nil {
		return nil, fmt.Errorf("POSTERM_QUESTION: %w", err)
	}
	quiet, _ := strconv.ParseInt(cfgOr("QUIET", "3"), 10, 64)
	if quiet < 2 {
		quiet = 2
	}
	e := &Engine{data: data, quiet: quiet, dialog: re, states: map[string]*LabState{}, drops: map[string]time.Time{}}
	e.scrubServer()
	return e, nil
}

// ---------------------------------------------------------------- tmux

// socket lives in the data dir: one data dir = one self-contained instance.
func (e *Engine) socket() string { return filepath.Join(e.data, "tmux.sock") }

func (e *Engine) tmuxCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-u", "-S", e.socket(), "-f", filepath.Join(e.data, "tmux.conf")}, args...)...)
	cmd.Env = append(labEnv(os.Environ()), "TERM=xterm-256color")
	return cmd
}

// labEnv is the environment the tmux server, hence every lab, inherits: ours
// minus the hub's own configuration. Whatever came from .env (the token, among
// others) and every POSTERM_/MONIA_ setting stays in the hub; a lab gets
// POSTERM_LAB and POSTERM_DATA from start(), nothing else of ours.
func labEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "TMUX" || k == "TERM" || hubOnly(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func hubOnly(key string) bool {
	return dotEnvKeys[key] || strings.HasPrefix(key, "POSTERM_") || strings.HasPrefix(key, "MONIA_")
}

// scrubServer removes the hub's configuration from the global environment of
// a tmux server started by an older version, which handed it everything. Labs
// started before keep what they had: rotate the token after upgrading.
func (e *Engine) scrubServer() {
	out, err := e.tmux("show-environment", "-g")
	if err != nil {
		return // no server yet: it will start with labEnv
	}
	for _, line := range strings.Split(out, "\n") {
		k, _, _ := strings.Cut(strings.TrimPrefix(line, "-"), "=")
		if k != "" && hubOnly(k) {
			e.tmux("set-environment", "-g", "-u", k)
		}
	}
}

func (e *Engine) tmux(args ...string) (string, error) {
	out, err := e.tmuxCmd(args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s : %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// start launches the lab command through a login+interactive shell, so that
// PATH is the one of a real terminal even when POSTERM runs under systemd. The
// explicit cd comes after the profile, which may have wandered elsewhere.
func (e *Engine) start(l Lab) error {
	shell := envOr("SHELL", "/bin/sh")
	run := shq(shell) + " -l"
	if l.Cmd != "" {
		run = shq(shell) + " -lic " + shq("cd "+shq(l.Dir)+" && "+l.Cmd)
	}
	run = "env POSTERM_LAB=" + l.ID + " POSTERM_DATA=" + shq(e.data) + " " + run
	os.Remove(filepath.Join(e.data, "etat", l.ID))
	if _, err := e.tmux("has-session", "-t", "="+l.ID); err == nil {
		_, err = e.tmux("respawn-pane", "-k", "-t", "="+l.ID+":", "-c", l.Dir, run)
		return err
	}
	_, err := e.tmux("new-session", "-d", "-s", l.ID, "-x", "160", "-y", "45", "-c", l.Dir, run)
	return err
}

// shellID names the bash that sits under a lab's terminal. Lab IDs are slugs,
// so the "__" can never collide with another lab.
func shellID(id string) string { return id + "__sh" }

// shell makes sure the lab's companion shell exists, in the lab's directory. It
// is not a lab: not in the registry, not watched. Typing `exit` ends it for good
// (no remain-on-exit); the next attach starts a fresh one.
func (e *Engine) shell(l Lab) error {
	id := shellID(l.ID)
	if out, err := e.tmux("display-message", "-p", "-t", "="+id+":", "#{pane_dead}"); err == nil {
		if strings.TrimSpace(out) != "1" {
			return nil
		}
		e.tmux("kill-session", "-t", "="+id)
	}
	if _, err := e.tmux("new-session", "-d", "-s", id, "-x", "160", "-y", "8", "-c", l.Dir,
		shq(envOr("SHELL", "/bin/sh"))+" -l"); err != nil {
		return err
	}
	target := "=" + id + ":"
	e.tmux("set-option", "-w", "-t", target, "remain-on-exit", "off")
	// The login profile may cd elsewhere, so the cd is typed once it has run
	// (shell-agnostic; the leading space keeps it out of the history).
	_, err := e.tmux("send-keys", "-t", target, "-l", " cd "+shq(l.Dir)+" && clear\r")
	return err
}

// ---------------------------------------------------------------- registry

func (e *Engine) labsFile() string { return filepath.Join(e.data, "labs.json") }

// loadLabs re-reads the registry from disk every time: the file is the API
// shared with the `posterm lab` CLI.
func (e *Engine) loadLabs() []Lab {
	var labs []Lab
	if b, err := os.ReadFile(e.labsFile()); err == nil {
		json.Unmarshal(b, &labs)
	}
	return labs
}

// lockLabs serialises every read-modify-write of labs.json across processes:
// the hub's API and a `posterm lab` typed in a shell edit the same file.
func (e *Engine) lockLabs() func() {
	f, err := os.OpenFile(e.labsFile()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return func() {}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

func (e *Engine) saveLabs(labs []Lab) error {
	b, _ := json.MarshalIndent(labs, "", "  ")
	tmp := e.labsFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.labsFile())
}

func (e *Engine) find(id string) (Lab, bool) {
	for _, l := range e.loadLabs() {
		if l.ID == id {
			return l, true
		}
	}
	return Lab{}, false
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func (e *Engine) Create(name, dir, cmd string) (Lab, error) {
	dir, err := filepath.Abs(expandHome(strings.TrimSpace(dir)))
	if err != nil {
		return Lab{}, err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return Lab{}, E("err.no_dir", "dir", dir)
	}
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(dir)
	}
	unlock := e.lockLabs()
	labs := e.loadLabs()
	base := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "labo"
	}
	id := base
	for n := 2; ; n++ {
		taken := false
		for _, l := range labs {
			taken = taken || l.ID == id
		}
		if !taken {
			break
		}
		id = base + "-" + strconv.Itoa(n)
	}
	l := Lab{ID: id, Name: name, Dir: dir, Cmd: strings.TrimSpace(cmd)}
	err = e.saveLabs(append(labs, l))
	unlock()
	if err != nil {
		return Lab{}, err
	}
	return l, e.start(l)
}

func (e *Engine) Delete(id string) error {
	defer e.lockLabs()()
	labs := e.loadLabs()
	for i, l := range labs {
		if l.ID == id {
			e.tmux("kill-session", "-t", "="+id)
			e.tmux("kill-session", "-t", "="+shellID(id))
			return e.saveLabs(append(labs[:i], labs[i+1:]...))
		}
	}
	return E("err.no_lab")
}

// ---------------------------------------------------------------- observation

type pane struct {
	activity int64
	dead     bool
	code     string
	seen     int
	cursorY  int
}

func (e *Engine) panes() map[string]pane {
	out, err := e.tmux("list-panes", "-a", "-F", "#{session_name}\t#{window_activity}\t#{pane_dead}\t#{pane_dead_status}\t#{session_attached}\t#{cursor_y}")
	panes := map[string]pane{}
	if err != nil { // no server running = no session at all
		return panes
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			continue
		}
		act, _ := strconv.ParseInt(f[1], 10, 64)
		seen, _ := strconv.Atoi(f[4])
		y, _ := strconv.Atoi(f[5])
		panes[f[0]] = pane{activity: act, dead: f[2] == "1", code: f[3], seen: seen, cursorY: y}
	}
	return panes
}

// tail keeps the last n non-empty lines, right-trimmed.
func tail(s string, n int) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimRight(line, " \t\r"); line != "" {
			keep = append(keep, line)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

// bare strips the box-drawing frames TUIs love, so `\?$` can see the text.
func bare(line string) string {
	return strings.TrimFunc(line, func(r rune) bool {
		return r == ' ' || r == '|' || (r >= 0x2500 && r <= 0x259F)
	})
}

// forced reads the state a lab declared about itself with `posterm state`.
// It holds until the screen moves again.
func (e *Engine) forced(id string, activity int64) (state, note string) {
	path := filepath.Join(e.data, "etat", id)
	st, err := os.Stat(path)
	if err != nil {
		return "", ""
	}
	if activity > st.ModTime().Unix()+2 {
		os.Remove(path)
		return "", ""
	}
	b, _ := os.ReadFile(path)
	state, note, _ = strings.Cut(string(b), "\n")
	if state != Question && state != Pose {
		return "", ""
	}
	return state, strings.TrimSpace(note)
}

func (e *Engine) poll() {
	labs := e.loadLabs()
	panes := e.panes()
	now := time.Now().Unix()

	// poll is the only writer of e.states: it may read the map without the
	// lock, works on copies (readers hold pointers to the old values under the
	// lock), and takes the lock only to swap the result in. Forking tmux while
	// holding it would stall Snapshot and every upload check for as long.
	next := make(map[string]*LabState, len(labs))
	order := make([]string, 0, len(labs))
	for _, l := range labs {
		s := &LabState{Since: now}
		if old := e.states[l.ID]; old != nil {
			*s = *old
		}
		s.Lab = l
		p, alive := panes[l.ID]
		state := Eteint
		s.Code, s.Note, s.Seen = "", "", p.seen
		if alive {
			if p.activity >= s.captured-1 { // the screen moved since we last looked
				if out, err := e.tmux("capture-pane", "-p", "-t", "="+l.ID+":"); err == nil {
					s.Screen, s.cursor = tail(out, 12), ""
					if rows := strings.Split(out, "\n"); p.cursorY < len(rows) {
						s.cursor = bare(rows[p.cursorY])
					}
				}
				s.captured = now
			}
			s.activity = p.activity
			switch {
			case p.dead:
				s.Code = p.code
			case now-p.activity < e.quiet:
				state = EnCours
			case promptRe.MatchString(s.cursor) || e.dialog.MatchString(s.Screen):
				state = Question
			default:
				state = Pose
			}
			if f, note := e.forced(l.ID, p.activity); f != "" && !p.dead {
				state, s.Note = f, note
			}
		}
		if state != s.State {
			s.State, s.Since = state, now
		}
		next[l.ID] = s
		order = append(order, l.ID)
	}
	e.mu.Lock()
	e.states, e.order = next, order
	e.mu.Unlock()
}

func (e *Engine) Run() {
	for {
		e.poll()
		time.Sleep(time.Second)
	}
}

func (e *Engine) Snapshot() []LabState {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]LabState, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, *e.states[id])
	}
	return out
}

// ---------------------------------------------------------------- HTTP API

// Handler is the whole surface of a machine. The hub mounts it under
// /m/<machine>/ — in-process for itself, through the tunnel for the others.
func (e *Engine) Handler() http.Handler {
	r := gin.New()
	r.Use(gin.Recovery())
	fail := func(c *gin.Context, code int, err error) { c.JSON(code, gin.H{"error": err.Error()}) }
	lab := func(c *gin.Context) (Lab, bool) {
		l, ok := e.find(c.Param("id"))
		if !ok {
			fail(c, 404, E("err.no_lab"))
		}
		return l, ok
	}

	r.GET("/api/labs", func(c *gin.Context) { c.JSON(200, e.Snapshot()) })

	r.POST("/api/labs", func(c *gin.Context) {
		var in struct{ Name, Dir, Cmd string }
		if err := c.BindJSON(&in); err != nil {
			return
		}
		l, err := e.Create(in.Name, in.Dir, in.Cmd)
		if err != nil {
			fail(c, 400, err)
			return
		}
		c.JSON(200, l)
	})

	r.POST("/api/labs/:id/start", func(c *gin.Context) {
		if l, ok := lab(c); ok {
			if err := e.start(l); err != nil {
				fail(c, 500, err)
				return
			}
			c.JSON(200, gin.H{"ok": true})
		}
	})

	r.POST("/api/labs/:id/stop", func(c *gin.Context) {
		if l, ok := lab(c); ok {
			e.tmux("kill-session", "-t", "="+l.ID)
			c.JSON(200, gin.H{"ok": true})
		}
	})

	r.DELETE("/api/labs/:id", func(c *gin.Context) {
		if l, ok := lab(c); ok {
			if err := e.Delete(l.ID); err != nil {
				fail(c, 500, err)
				return
			}
			c.JSON(200, gin.H{"ok": true})
		}
	})

	// keys types into a lab without opening it: {"text":"…","enter":true} or {"key":"C-c"}.
	keyName := regexp.MustCompile(`^[A-Za-z0-9-]{1,12}$`)
	r.POST("/api/labs/:id/keys", func(c *gin.Context) {
		l, ok := lab(c)
		if !ok {
			return
		}
		var in struct {
			Text  string
			Key   string
			Enter bool
		}
		if err := c.BindJSON(&in); err != nil {
			return
		}
		target := "=" + l.ID + ":"
		var err error
		if in.Text != "" {
			_, err = e.tmux("send-keys", "-t", target, "-l", "--", in.Text)
		}
		if err == nil && in.Key != "" && keyName.MatchString(in.Key) {
			_, err = e.tmux("send-keys", "-t", target, in.Key)
		}
		if err == nil && in.Enter {
			time.Sleep(150 * time.Millisecond) // let TUIs tell typing from pasting
			_, err = e.tmux("send-keys", "-t", target, "Enter")
		}
		if err != nil {
			fail(c, 409, err)
			return
		}
		c.JSON(200, gin.H{"ok": true})
	})

	// dirs completes a path prefix, for the "new lab" form.
	r.GET("/api/dirs", func(c *gin.Context) {
		p := expandHome(c.Query("p"))
		dir, prefix := filepath.Split(p)
		if dir == "" {
			dir = "/"
		}
		out := []string{}
		entries, _ := os.ReadDir(dir)
		for _, en := range entries {
			name := en.Name()
			if !strings.HasPrefix(name, prefix) || (prefix == "" && strings.HasPrefix(name, ".")) {
				continue
			}
			full := filepath.Join(dir, name)
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				if out = append(out, full); len(out) == 40 {
					break
				}
			}
		}
		c.JSON(200, out)
	})

	r.GET("/api/labs/:id/ws", func(c *gin.Context) {
		if l, ok := lab(c); ok {
			e.terminal(c, l.ID)
		}
	})

	r.GET("/api/labs/:id/sh", func(c *gin.Context) {
		if l, ok := lab(c); ok {
			if err := e.shell(l); err != nil {
				fail(c, 500, err)
				return
			}
			e.terminal(c, shellID(l.ID))
		}
	})

	// Files dropped on a lab's bash land in the bash's current directory. check
	// says where that is and which of the paths already exist there; upload then
	// writes into that very dir (passed back, so a `cd` in between changes nothing).
	r.POST("/api/labs/:id/sh/check", func(c *gin.Context) {
		l, ok := lab(c)
		if !ok {
			return
		}
		var in struct{ Paths []string }
		if err := c.BindJSON(&in); err != nil {
			return
		}
		dir, err := e.shellDir(l)
		if err != nil {
			// No bash yet (hidden in this browser, or never opened): a drop
			// still needs a cwd, so start it in the lab's dir.
			if e.shell(l) == nil {
				dir, err = e.shellDir(l)
			}
		}
		if err != nil {
			fail(c, 409, err)
			return
		}
		e.allowDrop(l.ID, dir)
		exists := []string{}
		for _, p := range in.Paths {
			if dst, err := inside(dir, p); err == nil {
				if _, err := os.Lstat(dst); err == nil {
					exists = append(exists, p)
				}
			}
		}
		c.JSON(200, gin.H{"dir": dir, "exists": exists})
	})

	r.POST("/api/labs/:id/sh/upload", func(c *gin.Context) {
		l, ok := lab(c)
		if !ok {
			return
		}
		dir := c.Query("dir")
		if !e.dropAllowed(l, dir) {
			fail(c, 403, E("err.stale_dir", "dir", dir))
			return
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() || !filepath.IsAbs(dir) {
			fail(c, 400, E("err.no_dir", "dir", dir))
			return
		}
		n, size, err := receive(c.Request, dir, c.Query("overwrite") == "1")
		if err != nil {
			fail(c, 400, E("err.rolled_back", "err", err.Error()))
			return
		}
		c.JSON(200, gin.H{"dir": dir, "files": n, "bytes": size})
	})
	return r
}

// unpack extracts the zip read from r into target. archive/zip needs to seek,
// so the zip goes through a hidden temp file next to its destination, removed
// afterwards. Entries are checked like any dropped path; symlinks are skipped,
// so is macOS's __MACOSX litter. A zip whose entries all sit in one folder
// named like the zip (Finder, `zip -r x.zip x`) does not nest it twice.
func unpack(u *undo, r io.Reader, dir, target string, flags int) (n int, size int64, err error) {
	tmp, err := os.CreateTemp(dir, ".posterm-drop-*.zip")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, r); err != nil {
		return 0, 0, err
	}
	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return 0, 0, E("err.bad_zip", "err", err.Error())
	}
	defer zr.Close()

	keep := func(name string) bool {
		return !strings.HasPrefix(name, "__MACOSX/") && filepath.Base(name) != ".DS_Store"
	}
	strip := filepath.Base(target) + "/"
	for _, f := range zr.File {
		if keep(f.Name) && !strings.HasPrefix(f.Name, strip) {
			strip = ""
			break
		}
	}
	for _, f := range zr.File { // all or nothing: every path is checked before writing
		if name := strings.TrimSuffix(strings.TrimPrefix(f.Name, strip), "/"); keep(f.Name) && name != "" {
			if _, err := inside(target, name); err != nil {
				return 0, 0, err
			}
		}
	}
	if err := u.mkdir(target); err != nil {
		return 0, 0, err
	}
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, strip)
		if !keep(f.Name) || name == "" || f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		dst, err := inside(target, strings.TrimSuffix(name, "/"))
		if err != nil {
			return n, size, err
		}
		if f.FileInfo().IsDir() {
			if err := u.mkdir(dst); err != nil {
				return n, size, err
			}
			continue
		}
		if err := u.mkdir(filepath.Dir(dst)); err != nil {
			return n, size, err
		}
		perm := os.FileMode(0o666)
		if f.Mode()&0o111 != 0 {
			perm = 0o777 // keep scripts executable
		}
		w, err := extract(u, f, dst, flags, perm)
		if err != nil {
			return n, size, fmt.Errorf("%s : %w", name, err)
		}
		n, size = n+1, size+w
	}
	return n, size, nil
}

func extract(u *undo, f *zip.File, dst string, flags int, perm os.FileMode) (int64, error) {
	src, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer src.Close()
	out, err := u.open(dst, flags, perm)
	if errors.Is(err, os.ErrExist) {
		return 0, E("err.exists")
	}
	if err != nil {
		return 0, err
	}
	w, err := io.Copy(out, src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return w, err
}

// undo remembers what an upload created, so that a cancelled or failed one can
// take it all back. Files it overwrote (only after a yes) cannot come back.
// base, when set, is the directory every write must stay in once symlinks
// are resolved.
type undo struct {
	base        string
	files, dirs []string
}

// resolvesInside: the deepest existing part of p, symlinks resolved, is base
// or under it. inside() checks the names; this checks the disk, where a link
// planted in the tree (by a lab, by an earlier drop) could point anywhere.
func resolvesInside(base, p string) bool {
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		return false
	}
	d := p
	for {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
	real, err := filepath.EvalSymlinks(d)
	if err != nil {
		return false
	}
	return real == root || strings.HasPrefix(real, root+string(os.PathSeparator))
}

// mkdir is MkdirAll that remembers the topmost directory it had to create.
func (u *undo) mkdir(p string) error {
	if u.base != "" && !resolvesInside(u.base, p) {
		return E("err.bad_path", "path", strconv.Quote(p))
	}
	top := ""
	for d := p; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || filepath.Dir(d) == d {
			break
		}
		top = d
	}
	if err := os.MkdirAll(p, 0o777); err != nil {
		return err
	}
	if top != "" {
		u.dirs = append(u.dirs, top)
	}
	return nil
}

// open is OpenFile that remembers the files it brought into existence.
func (u *undo) open(p string, flags int, perm os.FileMode) (*os.File, error) {
	if u.base != "" && !resolvesInside(u.base, filepath.Dir(p)) {
		return nil, E("err.bad_path", "path", strconv.Quote(p))
	}
	_, err := os.Lstat(p)
	fresh := err != nil
	f, err := os.OpenFile(p, flags|syscall.O_NOFOLLOW, perm) // never write through a link
	if err == nil && fresh {
		u.files = append(u.files, p)
	}
	return f, err
}

func (u *undo) rollback() {
	for i := len(u.files) - 1; i >= 0; i-- {
		os.Remove(u.files[i])
	}
	for i := len(u.dirs) - 1; i >= 0; i-- {
		os.RemoveAll(u.dirs[i]) // created by this upload: only its own files are in there
	}
}

// shellDir is the current directory of the lab's bash (or of whatever runs in it).
func (e *Engine) shellDir(l Lab) (string, error) {
	out, err := e.tmux("display-message", "-p", "-t", "="+shellID(l.ID)+":", "#{pane_current_path}")
	if dir := strings.TrimSpace(out); err == nil && dir != "" {
		return dir, nil
	}
	return "", E("err.no_shell")
}

// dropTTL bounds how long a dir handed out by sh/check stays a valid upload
// target: long enough to zip a big folder in the browser, short enough that
// the list stays tiny.
const dropTTL = 15 * time.Minute

// allowDrop records that sh/check told the browser to upload into dir.
func (e *Engine) allowDrop(id, dir string) {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, until := range e.drops {
		if now.After(until) {
			delete(e.drops, k)
		}
	}
	e.drops[id+"\n"+dir] = now.Add(dropTTL)
}

// dropAllowed: an upload may only target the bash's current dir, or a dir that
// sh/check handed out for this lab recently (the user may `cd` while the
// browser zips). Any other dir is refused, whatever the browser says.
func (e *Engine) dropAllowed(l Lab, dir string) bool {
	if cur, err := e.shellDir(l); err == nil && cur == dir {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	until, ok := e.drops[l.ID+"\n"+dir]
	return ok && time.Now().Before(until)
}

// inside resolves a relative path sent by the browser under dir, refusing
// anything that would land elsewhere.
func inside(dir, rel string) (string, error) {
	bad := rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\")
	for _, part := range strings.Split(rel, "/") {
		bad = bad || part == ".."
	}
	dst := filepath.Join(dir, rel)
	if bad || dst == filepath.Clean(dir) {
		return "", E("err.bad_path", "path", strconv.Quote(rel))
	}
	return dst, nil
}

// receive streams a drop into dir. The form is a "manifest" part first
// ({"files":[paths…],"dirs":[empty dirs…],"unzip":[paths…]}), then one "file"
// part per path, in order. Plain files go straight to disk; a path listed in
// unzip is a zip (a folder zipped by the browser, or a .zip dropped as is): it
// is unpacked into a directory named after it, and not kept.
func receive(req *http.Request, dir string, overwrite bool) (n int, size int64, err error) {
	u := &undo{base: dir}
	defer func() {
		if err != nil { // cancelled, or failed midway: a drop lands whole or not at all
			u.rollback()
			n, size = 0, 0
		}
	}()
	mr, err := req.MultipartReader()
	if err != nil {
		return 0, 0, err
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "manifest" {
		return 0, 0, E("err.no_manifest")
	}
	var man struct{ Files, Dirs, Unzip []string }
	if err := json.NewDecoder(io.LimitReader(part, 16<<20)).Decode(&man); err != nil {
		return 0, 0, E("err.bad_manifest", "err", err.Error())
	}
	for _, d := range man.Dirs {
		dst, err := inside(dir, d)
		if err != nil {
			return n, size, err
		}
		if err := u.mkdir(dst); err != nil {
			return n, size, err
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	unzip := map[string]bool{}
	for _, rel := range man.Unzip {
		unzip[rel] = true
	}
	for _, rel := range man.Files {
		part, err := mr.NextPart()
		if err != nil || part.FormName() != "file" {
			return n, size, E("err.missing_file", "path", rel)
		}
		dst, err := inside(dir, rel)
		if err != nil {
			return n, size, err
		}
		if unzip[rel] {
			k, w, err := unpack(u, part, dir, strings.TrimSuffix(dst, filepath.Ext(dst)), flags)
			n, size = n+k, size+w
			if err != nil {
				return n, size, fmt.Errorf("%s : %w", rel, err)
			}
			continue
		}
		if err := u.mkdir(filepath.Dir(dst)); err != nil {
			return n, size, err
		}
		f, err := u.open(dst, flags, 0o666)
		if errors.Is(err, os.ErrExist) {
			return n, size, E("err.exists_path", "path", rel)
		}
		if err != nil {
			return n, size, err
		}
		w, err := io.Copy(f, part)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst) // no half file
			return n, size, fmt.Errorf("%s : %w", rel, err)
		}
		n, size = n+1, size+w
	}
	return n, size, nil
}

// terminal bridges one browser xterm.js to one `tmux attach` running in a PTY.
// Binary frames are bytes both ways; text frames are {"cols":…,"rows":…}.
func (e *Engine) terminal(c *gin.Context, session string) {
	// Origin is checked by the hub before the request gets here.
	ws, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	size := func(cols, rows int) *pty.Winsize {
		clamp := func(v, def int) uint16 {
			if v < 2 || v > 1000 {
				v = def
			}
			return uint16(v)
		}
		return &pty.Winsize{Cols: clamp(cols, 120), Rows: clamp(rows, 32)}
	}
	cols, _ := strconv.Atoi(c.Query("cols"))
	rows, _ := strconv.Atoi(c.Query("rows"))
	cmd := e.tmuxCmd("attach-session", "-t", "="+session)
	tty, err := pty.StartWithSize(cmd, size(cols, rows))
	if err != nil {
		ws.Close(websocket.StatusInternalError, "pty")
		return
	}
	defer func() {
		cmd.Process.Kill()
		tty.Close()
		cmd.Wait()
	}()

	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := tty.Read(buf)
			if n > 0 && ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				return
			}
			if err != nil {
				ws.Close(websocket.StatusNormalClosure, "fin")
				return
			}
		}
	}()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var sz struct{ Cols, Rows int }
			if json.Unmarshal(data, &sz) == nil {
				pty.Setsize(tty, size(sz.Cols, sz.Rows))
			}
			continue
		}
		if _, err := tty.Write(data); err != nil {
			return
		}
	}
}
