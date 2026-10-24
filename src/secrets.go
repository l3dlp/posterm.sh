package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Secrets are what a lab must never read: the operator token, the spokes'
// join token and the VAPID key pair. They live in <data>/secrets/, a directory
// owned by whoever starts the hub (root, under systemd) with mode 0700, and are
// read before becomeUser drops privileges: after that the hub keeps them in
// memory only. A lab runs as POSTERM_USER, owns the data dir and could rename
// any file in it, but not a directory it cannot write to: secrets/ can be
// neither read, nor replaced, nor moved aside.
//
// Started as the labs' own user (no POSTERM_USER), the hub and the labs are the
// same user and nothing on disk can be hidden from them: the README says so.
type Secrets struct {
	Token        string // browsers and the API
	JoinToken    string // spokes, accepted on /join only
	VAPIDPublic  string
	VAPIDPrivate string
}

type vapidKeys struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

func secretsDir(data string) string { return filepath.Join(data, "secrets") }

// loadSecrets reads, migrates or creates the hub's secrets. It must run before
// privileges are dropped and before handOver touches the data dir.
func loadSecrets(data string) Secrets {
	dir := secretsDir(data)
	if err := os.MkdirAll(data, 0o700); err != nil {
		log.Fatal(err)
	}
	ensureSecretsDir(dir)

	var s Secrets
	s.Token = secret(dir, "token", cfg("TOKEN"), filepath.Join(data, "token"), "log.token")
	s.JoinToken = secret(dir, "join-token", cfg("JOIN_TOKEN"), "", "log.join_token")

	keysFile := filepath.Join(dir, "vapid.json")
	var keys vapidKeys
	if b, err := readSecret(keysFile); err == nil {
		json.Unmarshal(b, &keys)
	}
	if keys.Public == "" || keys.Private == "" {
		keys = legacyVAPID(filepath.Join(data, "push.json"))
	}
	if keys.Public == "" || keys.Private == "" {
		var err error
		if keys.Private, keys.Public, err = webpush.GenerateVAPIDKeys(); err != nil {
			log.Fatal(err)
		}
	}
	b, _ := json.MarshalIndent(keys, "", "  ")
	writeSecret(keysFile, b)
	s.VAPIDPublic, s.VAPIDPrivate = keys.Public, keys.Private
	return s
}

// ensureSecretsDir makes dir a private directory of ours. A secrets/ that
// someone else made (a lab, before this version existed) is moved aside, never
// trusted: whoever made it chose its content.
func ensureSecretsDir(dir string) {
	st, err := os.Lstat(dir)
	if err == nil {
		owner := int(st.Sys().(*syscall.Stat_t).Uid)
		if st.IsDir() && owner == os.Geteuid() {
			if st.Mode().Perm() != 0o700 {
				os.Chmod(dir, 0o700)
			}
			return
		}
		aside := dir + ".rejected-" + strconv.FormatInt(time.Now().Unix(), 10)
		log.Print(T("log.secrets_rejected", "path", dir, "aside", aside))
		if err := os.Rename(dir, aside); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		log.Fatal(err)
	}
}

// secret returns the configured value, else the stored one, else the legacy
// file's (moved into secrets/), else a new random one, announced once.
func secret(dir, name, configured, legacy, announce string) string {
	if configured != "" {
		return configured
	}
	path := filepath.Join(dir, name)
	if b, err := readSecret(path); err == nil && len(bytes.TrimSpace(b)) >= 16 {
		return string(bytes.TrimSpace(b))
	}
	if legacy != "" {
		if b, err := os.ReadFile(legacy); err == nil && len(bytes.TrimSpace(b)) >= 16 {
			t := string(bytes.TrimSpace(b))
			writeSecret(path, []byte(t+"\n"))
			os.Remove(legacy)
			log.Print(T("log.secret_moved", "from", legacy, "to", path))
			return t
		}
	}
	t := randomToken()
	writeSecret(path, []byte(t+"\n"))
	log.Print(T(announce, "token", t, "path", path))
	return t
}

func randomToken() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

// readSecret reads a regular file of ours that nobody else can read.
func readSecret(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || int(st.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() || st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: not a private file of ours", path)
	}
	return os.ReadFile(path)
}

// writeSecret replaces path atomically with a 0600 file.
func writeSecret(path string, b []byte) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		log.Fatal(err)
	}
	if _, err := tmp.Write(b); err != nil {
		log.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		log.Fatal(err)
	}
}

// legacyVAPID takes the key pair out of an old push.json, which kept it next
// to the subscriptions. The file is rewritten without it.
func legacyVAPID(path string) vapidKeys {
	b, err := os.ReadFile(path)
	if err != nil {
		return vapidKeys{}
	}
	var old map[string]json.RawMessage
	if json.Unmarshal(b, &old) != nil {
		return vapidKeys{}
	}
	var keys vapidKeys
	json.Unmarshal(old["public"], &keys.Public)
	json.Unmarshal(old["private"], &keys.Private)
	if keys.Private == "" {
		return vapidKeys{}
	}
	delete(old, "public")
	delete(old, "private")
	rest, _ := json.MarshalIndent(old, "", "  ")
	if err := os.WriteFile(path, rest, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Print(err)
	}
	log.Print(T("log.secret_moved", "from", path+" (VAPID)", "to", "secrets/vapid.json"))
	return keys
}
