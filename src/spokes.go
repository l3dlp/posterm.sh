package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Per-machine join tokens. `posterm spoke add <name>` prints a token bound to
// one machine name; the hub keeps only its SHA-256 in <data>/spokes.json. The
// file holds no secret, so the hub can re-read it after dropping privileges,
// and it is owned by whoever ran the command (root, on a hub started as root):
// in the sticky data dir a lab can neither edit it nor swap it for its own.
// A spoke that presents a name's token can join under that name only.

type spokeEntry struct {
	Hash    string `json:"hash"` // hex SHA-256 of the token
	Created int64  `json:"created"`
}

func spokesFile(data string) string { return filepath.Join(data, "spokes.json") }

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func loadSpokes(data string) map[string]spokeEntry {
	spokes := map[string]spokeEntry{}
	if b, err := os.ReadFile(spokesFile(data)); err == nil {
		json.Unmarshal(b, &spokes)
	}
	return spokes
}

func saveSpokes(data string, spokes map[string]spokeEntry) error {
	b, _ := json.MarshalIndent(spokes, "", "  ")
	tmp, err := os.CreateTemp(data, ".spokes.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), spokesFile(data))
}

func spokeName(raw string) string {
	return strings.Trim(notSlug.ReplaceAllString(strings.ToLower(raw), "-"), "-")
}

// spokeCommand runs `posterm spoke add|revoke|list`, on the hub's data dir,
// with the privileges it was started with (before becomeUser drops them).
func spokeCommand(data string, args []string) {
	if len(args) == 0 {
		log.Fatal(T("cli.spoke_usage"))
	}
	spokes := loadSpokes(data)
	switch args[0] {
	case "list":
		names := make([]string, 0, len(spokes))
		for name := range spokes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("%-24s %s\n", name, time.Unix(spokes[name].Created, 0).Format("2006-01-02 15:04"))
		}
		if len(names) == 0 {
			fmt.Println(T("cli.spoke_none"))
		}
	case "add", "revoke":
		if len(args) != 2 || spokeName(args[1]) == "" {
			log.Fatal(T("cli.spoke_usage"))
		}
		name := spokeName(args[1])
		if args[0] == "revoke" {
			if _, ok := spokes[name]; !ok {
				log.Fatal(T("cli.spoke_unknown", "name", name))
			}
			delete(spokes, name)
			if err := saveSpokes(data, spokes); err != nil {
				log.Fatal(err)
			}
			fmt.Print(T("cli.spoke_revoked", "name", name))
			return
		}
		token := name + "." + randomToken()
		spokes[name] = spokeEntry{Hash: hashToken(token), Created: time.Now().Unix()}
		if err := saveSpokes(data, spokes); err != nil {
			log.Fatal(err)
		}
		fmt.Print(T("cli.spoke_added", "name", name, "token", token))
	default:
		log.Fatal(T("cli.spoke_usage"))
	}
}

// spokeRegistry is the hub's view of spokes.json, re-read when it changes.
type spokeRegistry struct {
	data    string
	mod     time.Time
	entries map[string]spokeEntry
}

func (s *spokeRegistry) refresh() (revoked []string) {
	st, err := os.Stat(spokesFile(s.data))
	var mod time.Time
	if err == nil {
		mod = st.ModTime()
	}
	if s.entries != nil && mod.Equal(s.mod) {
		return nil
	}
	next := loadSpokes(s.data)
	for name := range s.entries {
		if _, ok := next[name]; !ok {
			revoked = append(revoked, name)
		}
	}
	s.entries, s.mod = next, mod
	slices.Sort(revoked)
	return revoked
}

// check says whether token opens /join for name: "spoke" (its own token),
// "legacy" (the hub-wide join token or the operator token, accepted with a
// warning while spokes move to their own), or "" (refused). A name that has
// its own token can't be joined with a legacy one anymore.
func (s *spokeRegistry) check(name, token, joinToken, operatorToken string) string {
	if token == "" {
		return ""
	}
	if e, ok := s.entries[name]; ok {
		if same(hashToken(token), e.Hash) {
			return "spoke"
		}
		return ""
	}
	if same(token, joinToken) || same(token, operatorToken) {
		return "legacy"
	}
	return ""
}
