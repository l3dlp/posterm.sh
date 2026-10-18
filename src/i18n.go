package main

import (
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Every user-facing string lives in web/i18n/<lang>.json, shared by the Go side
// (T), the page (titled) and the browser (/i18n.js). POSTERM_LANG picks one per
// installation; English is the default and the fallback for any missing key.

const fallbackLang = "en"

var (
	i18nOnce sync.Once
	langName string
	dicts    = map[string]map[string]string{}
)

func loadI18n() {
	entries, err := webFS.ReadDir("web/i18n")
	if err != nil {
		log.Fatal(err)
	}
	for _, en := range entries {
		b, err := webFS.ReadFile("web/i18n/" + en.Name())
		if err != nil {
			log.Fatal(err)
		}
		d := map[string]string{}
		if err := json.Unmarshal(b, &d); err != nil {
			log.Fatalf("i18n %s: %v", en.Name(), err)
		}
		dicts[strings.TrimSuffix(en.Name(), ".json")] = d
	}
	langName = cfgOr("LANG", fallbackLang)
	if dicts[langName] == nil {
		log.Print(tIn(fallbackLang, "log.bad_lang", "lang", langName, "fallback", fallbackLang))
		langName = fallbackLang
	}
	for name, d := range dicts {
		var missing []string
		for k := range dicts[fallbackLang] {
			if _, ok := d[k]; !ok {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			log.Print(tIn(langName, "log.keys_missing", "n", strconv.Itoa(len(missing)), "lang", name, "keys", strings.Join(missing, ", ")))
		}
	}
}

// lang is the installation's language. Resolved on first use, after the .env.
func lang() string {
	i18nOnce.Do(loadI18n)
	return langName
}

// dict is the active dictionary, missing keys filled from the fallback.
func dict() map[string]string {
	out := map[string]string{}
	for k, v := range dicts[fallbackLang] {
		out[k] = v
	}
	for k, v := range dicts[lang()] {
		out[k] = v
	}
	return out
}

// T translates key, replacing {name} by the value that follows name in kv.
func T(key string, kv ...string) string {
	return tIn(lang(), key, kv...)
}

func tIn(l, key string, kv ...string) string {
	s, ok := dicts[l][key]
	if !ok {
		if s, ok = dicts[fallbackLang][key]; !ok {
			s = key
		}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		s = strings.ReplaceAll(s, "{"+kv[i]+"}", kv[i+1])
	}
	return s
}

// E is T as an error.
func E(key string, kv ...string) error { return errors.New(T(key, kv...)) }
