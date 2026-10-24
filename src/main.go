// POSTERM — a wall of post-its, one per lab (a dir + a command in tmux),
// across as many machines as you like. One binary, four faces:
//
//	posterm                       the hub: web wall + this machine's labs
//	posterm join [hub-url]        a spoke: publishes this machine's labs to the hub
//	posterm lab <dir> [cmd…]      adds and starts a lab on this machine
//	posterm state <question|idle> [text…]   from inside a lab: say where it is
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// cfg reads the setting POSTERM_<key>, then its pre-rename spelling MONIA_<key>,
// so that an existing .env, service or lab keeps working untouched.
func cfg(key string) string {
	if v := os.Getenv("POSTERM_" + key); v != "" {
		return v
	}
	return os.Getenv("MONIA_" + key)
}

func cfgOr(key, def string) string {
	if v := cfg(key); v != "" {
		return v
	}
	return def
}

// dotEnvKeys are the names loadDotEnv set: the hub's configuration, which
// the labs never inherit (see labEnv).
var dotEnvKeys = map[string]bool{}

// loadDotEnv reads KEY=VALUE lines; the real environment always wins.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
			dotEnvKeys[k] = true
		}
	}
}

func dataDir() string {
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, ".posterm")
	if _, err := os.Stat(def); err != nil {
		if _, err := os.Stat(filepath.Join(home, ".monia")); err == nil {
			def = filepath.Join(home, ".monia") // pre-rename default
		}
	}
	dir, err := filepath.Abs(expandHome(cfgOr("DATA", def)))
	if err != nil {
		log.Fatal(err)
	}
	return dir
}

func machineName() string {
	host, _ := os.Hostname()
	name := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(cfgOr("NAME", host)), "-"), "-")
	if name == "" {
		name = "machine"
	}
	return name
}

func usage() {
	fmt.Fprint(os.Stderr, T("cli.usage", "title", cfgOr("TITLE", "POSTERM")))
	os.Exit(2)
}

func main() {
	log.SetFlags(log.Ltime)
	gin.SetMode(gin.ReleaseMode)
	loadDotEnv(".env")
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}

	// `posterm state` is run by the labs themselves: the filesystem is the IPC.
	// `posterm etat … pose` is the French spelling of `posterm state … idle`.
	if cmd == "etat" || cmd == "state" {
		lab, data := cfg("LAB"), cfg("DATA")
		if len(args) > 0 && args[0] == "idle" {
			args[0] = Pose
		}
		if len(args) == 0 || (args[0] != Question && args[0] != Pose) || lab == "" || data == "" {
			log.Fatal(T("cli.etat_usage"))
		}
		body := args[0] + "\n" + strings.Join(args[1:], " ")
		if err := os.WriteFile(filepath.Join(data, "etat", filepath.Base(lab)), []byte(body), 0o600); err != nil {
			log.Fatal(err)
		}
		return
	}

	// The hub reads its secrets while it may still be root, before the data
	// dir is handed over to the labs' user: see secrets.go.
	var sec Secrets
	data := becomeUser(func(data string) {
		if cmd == "" {
			sec = loadSecrets(data)
		}
	})
	engine, err := NewEngine(data)
	if err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "":
		runHub(engine, data, sec)
	case "join":
		hub := cfg("HUB")
		if len(args) > 0 {
			hub = args[0]
		}
		if hub == "" || cfg("TOKEN") == "" {
			log.Fatal(T("cli.join_needs"))
		}
		runSpoke(engine, hub, cfg("TOKEN"))
	case "lab":
		if len(args) == 0 {
			usage()
		}
		l, err := engine.Create("", args[0], strings.Join(args[1:], " "))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(T("cli.lab_started", "id", l.ID, "dir", l.Dir, "sock", engine.socket()))
	default:
		usage()
	}
}
