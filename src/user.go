package main

import (
	"bufio"
	"errors"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// becomeUser switches the whole process to POSTERM_USER / POSTERM_GROUP (from the
// environment or the .env), so that systemd can start us as root while the labs
// run as whoever the installation chose: posterm, www-data… or root itself. It
// must run before anything touches tmux or the data dir, and it returns the
// data dir, resolved with the new user's HOME. prepare runs on the data dir
// before it is handed over and before privileges are dropped.
func becomeUser(prepare func(data string)) string {
	name, group := cfg("USER"), cfg("GROUP")
	me, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	if name == "" && group == "" {
		ownEnv(me)
		data := dataDir() // stay who we are
		checkSocket(data, os.Getuid())
		prepare(data)
		return data
	}
	if name == "" {
		name = me.Username
	}
	u, err := user.Lookup(name)
	if err != nil {
		log.Fatalf("POSTERM_USER: %v", err)
	}
	gid := u.Gid
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			log.Fatalf("POSTERM_GROUP: %v", err)
		}
		gid = g.Gid
	}
	if u.Uid == me.Uid && gid == me.Gid {
		ownEnv(me)
		data := dataDir()
		checkSocket(data, os.Getuid())
		prepare(data)
		return data
	}
	if me.Uid != "0" {
		log.Fatal(T("log.need_root", "name", name))
	}

	// The labs inherit this environment: it must be the new user's, not root's.
	os.Setenv("HOME", u.HomeDir)
	os.Setenv("USER", u.Username)
	os.Setenv("LOGNAME", u.Username)
	os.Setenv("SHELL", labShell(u.Username))
	for _, k := range []string{"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "MAIL"} {
		os.Unsetenv(k)
	}

	uid, _ := strconv.Atoi(u.Uid)
	g, _ := strconv.Atoi(gid)
	data := dataDir()
	prepare(data)
	handOver(data, uid, g)

	groups := []int{g}
	if ids, err := u.GroupIds(); err == nil {
		for _, id := range ids {
			if n, err := strconv.Atoi(id); err == nil && n != g {
				groups = append(groups, n)
			}
		}
	}
	// Order matters: groups and gid while we are still root, uid last.
	if err := syscall.Setgroups(groups); err != nil {
		log.Fatalf("setgroups : %v", err)
	}
	if err := syscall.Setgid(g); err != nil {
		log.Fatalf("setgid : %v", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		log.Fatalf("setuid : %v", err)
	}
	return data
}

// checkSocket refuses to go on if a tmux server of another user still holds
// the socket: the labs would keep running as that user, whatever POSTERM_USER
// says. The owner is the server process's, not the socket file's: a chown of
// the data dir must not fool us.
func checkSocket(data string, uid int) {
	sock := filepath.Join(data, "tmux.sock")
	st, err := os.Stat(sock)
	if err != nil {
		return
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			os.Remove(sock) // stale: nobody behind it
		}
		return
	}
	c.Close()
	owner := tmuxOwner(sock)
	if owner < 0 {
		owner = int(st.Sys().(*syscall.Stat_t).Uid)
	}
	if owner == uid {
		return
	}
	who := strconv.Itoa(owner)
	if u, err := user.LookupId(who); err == nil {
		who = u.Username
	}
	log.Fatal(T("log.tmux_owner", "who", who, "sock", sock))
}

// tmuxOwner is the uid of the tmux server process behind sock, -1 if unknown.
func tmuxOwner(sock string) int {
	out, err := exec.Command("tmux", "-S", sock, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		return -1
	}
	st, err := os.Stat("/proc/" + strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return int(st.Sys().(*syscall.Stat_t).Uid)
}

// handOver gives the data dir to the new user: the regular files POSTERM keeps
// there and etat/, nothing deeper. Never the tmux socket: it belongs to its
// server. Never secrets/: it stays root's.
//
// The data dir itself stays root's, group-writable by the labs' group and
// sticky (01770): the hub (now that user) can create, replace and remove its
// own files in it, while nobody but root can rename or remove secrets/. In a
// directory the labs owned, a mv of secrets/ would only need write access to
// the data dir.
func handOver(data string, uid, gid int) {
	checkSocket(data, uid)
	if err := os.MkdirAll(filepath.Join(data, "etat"), 0o700); err != nil {
		log.Fatal(err)
	}
	if err := os.Lchown(data, 0, gid); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(data, 0o770|os.ModeSticky); err != nil {
		log.Fatal(err)
	}
	chown := func(p string) {
		if err := os.Lchown(p, uid, gid); err != nil {
			log.Printf("chown %s : %v", p, err)
		}
	}
	for _, dir := range []string{data, filepath.Join(data, "etat")} {
		if dir != data {
			chown(dir)
		}
		entries, _ := os.ReadDir(dir)
		for _, en := range entries {
			if en.Type().IsRegular() {
				chown(filepath.Join(dir, en.Name()))
			}
		}
	}
}

// ownEnv fills in what a service manager leaves out when we keep our own
// identity: systemd starts the hub with neither HOME nor SHELL, and the labs
// would then run `/bin/sh -lic`, which reads no .bashrc and finds no claude in
// ~/.bun/bin or ~/.local/bin. What is already set is kept.
func ownEnv(me *user.User) {
	for k, v := range map[string]string{"HOME": me.HomeDir, "USER": me.Username, "LOGNAME": me.Username} {
		if os.Getenv(k) == "" && v != "" {
			os.Setenv(k, v)
		}
	}
	if os.Getenv("SHELL") == "" {
		os.Setenv("SHELL", labShell(me.Username))
	}
}

// labShell is the user's login shell, unless it is a service account's
// nologin/false: the labs need a real one.
func labShell(name string) string {
	sh := loginShell(name)
	if base := filepath.Base(sh); sh != "" && base != "nologin" && base != "false" {
		return sh
	}
	for _, sh := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			log.Print(T("log.no_login_shell", "name", name, "shell", sh))
			return sh
		}
	}
	return "/bin/sh"
}

// loginShell reads the user's shell from /etc/passwd (os/user does not say).
func loginShell(name string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Split(sc.Text(), ":"); len(fields) == 7 && fields[0] == name {
			return fields[6]
		}
	}
	return ""
}
