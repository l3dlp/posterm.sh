package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func readDict(t *testing.T, lang string) map[string]string {
	t.Helper()
	b, err := webFS.ReadFile("web/i18n/" + lang + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]string
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s.json: %v", lang, err)
	}
	return d
}

// Every user-facing string lives in both dictionaries, with the same keys.
func TestDictionariesHaveTheSameKeys(t *testing.T) {
	en, fr := readDict(t, "en"), readDict(t, "fr")
	var onlyEn, onlyFr []string
	for k := range en {
		if _, ok := fr[k]; !ok {
			onlyEn = append(onlyEn, k)
		}
	}
	for k := range fr {
		if _, ok := en[k]; !ok {
			onlyFr = append(onlyFr, k)
		}
	}
	sort.Strings(onlyEn)
	sort.Strings(onlyFr)
	if len(onlyEn)+len(onlyFr) > 0 {
		t.Errorf("only in en.json: %v\nonly in fr.json: %v", onlyEn, onlyFr)
	}
}

// Every literal key passed to T()/E() in Go, t()/tn() in app.js and {{t:…}}
// in the page exists in the dictionaries.
func TestEveryUsedKeyExists(t *testing.T) {
	en := readDict(t, "en")
	used := map[string]string{}
	scan := func(path string, re *regexp.Regexp) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = path
		}
	}
	goKey := regexp.MustCompile(`\b[TE]\("([a-z0-9_]+\.[a-z0-9_.]+)"`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			scan(f, goKey)
		}
	}
	scan("web/app.js", regexp.MustCompile(`\bt\('([a-z0-9_]+\.[a-z0-9_.]+)'`))
	// tn() picks a plural form: the key needs its _one and _other variants.
	plural := map[string]string{}
	b, _ := os.ReadFile("web/app.js")
	for _, m := range regexp.MustCompile(`\btn\('([a-z0-9_]+\.[a-z0-9_.]+)'`).FindAllStringSubmatch(string(b), -1) {
		plural[m[1]+"_one"], plural[m[1]+"_other"] = "web/app.js", "web/app.js"
	}
	for k, where := range plural {
		used[k] = where
	}
	scan("web/index.html", regexp.MustCompile(`\{\{t:([a-z0-9_.]+)\}\}`))
	var missing []string
	for k, where := range used {
		if _, ok := en[k]; !ok {
			missing = append(missing, k+" ("+where+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("keys used but missing from the dictionaries: %v", missing)
	}
}
