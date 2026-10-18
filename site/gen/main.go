// Command gen builds posterm.sh: the two home pages from their HTML bodies and
// the design page from docs/design.md, all in one layout. Stdlib only, no
// dependency in go.mod.
//
//	go run ./site/gen        (from the repo root; site/build.sh does it)
//
// The markdown is the subset docs/design.md uses: ATX headings, paragraphs,
// "- " lists (one nested level), "1. " lists, fenced code, tables, inline
// `code`, **bold**, *italic* and [links](url).
package main

import (
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The one place the repository URL lives. Every page gets it through {{repo}}.
const repo = "https://github.com/l3dlp/posterm.sh"

const site = "https://posterm.sh/"

type page struct {
	src, out, lang, root, path string
	title, desc                string
	design                     bool
}

var pages = []page{
	{src: "site/gen/home.en.html", out: "site/index.html", lang: "en", root: "", path: "",
		title: "POSTERM — a wall of post-its, every one a real terminal",
		desc:  "Run claude, codex, crush or bash in tmux and read all of them on one wall: one post-it per job, colored by whether it needs you. One Go binary, no database, MIT."},
	{src: "site/gen/home.fr.html", out: "site/fr/index.html", lang: "fr", root: "../", path: "fr/",
		title: "POSTERM — un mur de post-its, chacun un vrai terminal",
		desc:  "Lance claude, codex, crush ou bash dans tmux et lis-les tous sur un seul mur : un post-it par job, coloré selon qu'il a besoin de toi. Un binaire Go, pas de base de données, MIT."},
	{src: "docs/design.md", out: "site/design/index.html", lang: "en", root: "../", path: "design/", design: true,
		title: "Why POSTERM is built this way",
		desc:  "Every design choice behind POSTERM — tmux as the job queue, one static binary, spokes that dial out, state read off the screen — with what was turned down and what it costs."},
}

var words = map[string]map[string]string{
	"en": {"skip": "Skip to content", "how": "How it works", "install": "Install", "design": "Design", "lang": "FR", "langHref": "fr/", "langName": "Version française",
		"toc": "Contents", "edit": "Source of this page", "license": "MIT license", "readme": "README", "home": "Home"},
	"fr": {"skip": "Aller au contenu", "how": "Comment ça marche", "install": "Installer", "design": "Design (en)", "lang": "EN", "langHref": "", "langName": "English version",
		"toc": "Sommaire", "edit": "Source de cette page", "license": "licence MIT", "readme": "README", "home": "Accueil"},
}

func main() {
	layout := read("site/gen/layout.html")
	for _, p := range pages {
		w := words[p.lang]
		var body, extraHead string
		if p.design {
			md, toc := markdown(read(p.src))
			body = designBody(md, toc, w)
			extraHead = ""
		} else {
			body = read(p.src)
		}
		alt := ""
		if !p.design {
			alt = fmt.Sprintf(`<link rel="alternate" hreflang="en" href="%s">
<link rel="alternate" hreflang="fr" href="%sfr/">
<link rel="alternate" hreflang="x-default" href="%s">`, site, site, site)
		}
		langHref := p.root + w["langHref"]
		if p.design {
			langHref = p.root + "fr/"
			w = copyWith(w, "lang", "FR")
		}
		out := strings.NewReplacer(
			"{{head}}", extraHead,
			"{{alternates}}", alt,
		).Replace(layout)
		if !p.design {
			out = strings.Replace(out, "{{body}}", body, 1) // home bodies use the placeholders too
		}
		out = strings.NewReplacer(
			"{{lang}}", p.lang,
			"{{title}}", html.EscapeString(p.title),
			"{{desc}}", html.EscapeString(p.desc),
			"{{canonical}}", site+p.path,
			"{{og_locale}}", map[string]string{"en": "en_US", "fr": "fr_FR"}[p.lang],
			"{{root}}", p.root,
			"{{home}}", orDot(p.root+map[string]string{"en": "", "fr": "fr/"}[p.lang]),
			"{{repo}}", repo,
			"{{site}}", site,
			"{{t.skip}}", w["skip"], "{{t.how}}", w["how"], "{{t.install}}", w["install"],
			"{{t.design}}", w["design"], "{{t.lang}}", w["lang"], "{{t.langCode}}", strings.ToLower(w["lang"]), "{{t.langName}}", w["langName"],
			"{{t.license}}", w["license"], "{{t.readme}}", w["readme"], "{{t.home}}", w["home"],
			"{{langHref}}", orDot(langHref),
		).Replace(out)
		if i := strings.Index(strings.Replace(out, "{{body}}", "", 1), "{{"); i >= 0 {
			log.Fatalf("%s: unreplaced placeholder near %q", p.out, out[i:][:40])
		}
		out = strings.Replace(out, "{{body}}", body, 1) // the design doc is inserted as is: it may quote {{…}}
		if err := os.MkdirAll(filepath.Dir(p.out), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(p.out, []byte(out), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", p.out)
	}
}

// orDot keeps a link to the current dir from being an empty href.
func orDot(href string) string {
	if href == "" {
		return "./"
	}
	return href
}

func copyWith(m map[string]string, k, v string) map[string]string {
	n := map[string]string{}
	for a, b := range m {
		n[a] = b
	}
	n[k] = v
	return n
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	return string(b)
}

type heading struct{ id, text string }

func designBody(md string, toc []heading, w map[string]string) string {
	var b strings.Builder
	head := ""
	if i := strings.Index(md, "<h2"); i > 0 {
		head, md = md[:i], md[i:] // the title and its intro come before the contents on a phone
	}
	b.WriteString(`<div class="doc wrap">` + "\n")
	b.WriteString(`<header class="doc-head prose">` + head + "</header>\n")
	b.WriteString(`<nav class="toc" aria-label="` + w["toc"] + `"><details open><summary>` + w["toc"] + "</summary><ol>\n")
	for _, h := range toc {
		fmt.Fprintf(&b, `<li><a href="#%s">%s</a></li>`+"\n", h.id, h.text)
	}
	b.WriteString("</ol></details></nav>\n")
	b.WriteString(`<article class="prose">` + "\n" + md + "</article>\n</div>\n")
	return b.String()
}

// ---------------------------------------------------------------- markdown

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reUL      = regexp.MustCompile(`^(\s*)[-*]\s+(.*)$`)
	reOL      = regexp.MustCompile(`^(\s*)\d+\.\s+(.*)$`)
	reCode    = regexp.MustCompile("`([^`]+)`")
	reBold    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic  = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*?)\*`)
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reAnchor  = regexp.MustCompile(`^\[[^\]]+\]\(#[^)]+\)$`)
	reSlugDel = regexp.MustCompile(`[^\p{L}\p{N} _-]`)
)

func slug(s string) string {
	s = strings.ToLower(stripInline(s))
	s = reSlugDel.ReplaceAllString(s, "")
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "-")
}

// stripInline drops markdown markup, for slugs and the TOC.
func stripInline(s string) string {
	s = reLink.ReplaceAllString(s, "$1")
	return strings.NewReplacer("`", "", "**", "", "*", "").Replace(s)
}

// inline renders one line of text: code spans first, kept out of the other rules.
func inline(s string) string {
	var spans []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})
	s = html.EscapeString(s)
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		p := reLink.FindStringSubmatch(m)
		href := html.UnescapeString(p[2])
		ext := ""
		if strings.HasPrefix(href, "http") {
			ext = ` rel="noopener"`
		} else if !strings.HasPrefix(href, "#") {
			href = repo + "/blob/main/" + strings.TrimPrefix(href, "../") // repo-relative paths
		}
		return `<a href="` + html.EscapeString(href) + `"` + ext + `>` + p[1] + `</a>`
	})
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "$1<em>$2</em>")
	for i, sp := range spans {
		s = strings.Replace(s, fmt.Sprintf("\x00%d\x00", i), sp, 1)
	}
	return s
}

type item struct {
	text string
	kids []string
}

// markdown converts the supported subset and returns the ## headings for the TOC.
func markdown(src string) (string, []heading) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out strings.Builder
	var toc []heading
	var para []string
	flush := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	title := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, "```"):
			flush()
			lang := strings.TrimSpace(strings.TrimPrefix(t, "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			cls := ""
			if lang != "" {
				cls = ` class="lang-` + html.EscapeString(lang) + `"`
			}
			out.WriteString(`<pre tabindex="0"><code` + cls + ">" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case reHeading.MatchString(t):
			flush()
			m := reHeading.FindStringSubmatch(t)
			n := len(m[1])
			if n == 1 && !title {
				title = true
				out.WriteString("<h1>" + inline(m[2]) + "</h1>\n")
				continue
			}
			id := slug(m[2])
			if n == 2 {
				toc = append(toc, heading{id, html.EscapeString(stripInline(m[2]))})
			}
			fmt.Fprintf(&out, `<h%d id="%s">%s <a class="hash" href="#%s" aria-label="link to this section">#</a></h%d>`+"\n", n, id, inline(m[2]), id, n)
		case strings.HasPrefix(t, "|") && i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|") && strings.Contains(lines[i+1], "---"):
			flush()
			cells := func(s string) []string {
				s = strings.Trim(strings.TrimSpace(s), "|")
				c := strings.Split(s, "|")
				for k := range c {
					c[k] = strings.TrimSpace(c[k])
				}
				return c
			}
			out.WriteString(`<div class="table" tabindex="0"><table><thead><tr>`)
			for _, c := range cells(t) {
				out.WriteString("<th>" + inline(c) + "</th>")
			}
			out.WriteString("</tr></thead><tbody>\n")
			for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				out.WriteString("<tr>")
				for _, c := range cells(lines[i]) {
					out.WriteString("<td>" + inline(c) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
			i--
			out.WriteString("</tbody></table></div>\n")
		case reUL.MatchString(l) && indent(l) == 0, reOL.MatchString(l) && indent(l) == 0:
			flush()
			ordered := reOL.MatchString(l)
			items := []item{}
			for ; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) == "" {
					// a blank line ends the list unless the next line carries on with it
					if i+1 < len(lines) && (indent(lines[i+1]) > 0 || sameList(lines[i+1], ordered)) {
						continue
					}
					break
				}
				switch {
				case indent(l) == 0 && sameList(l, ordered):
					items = append(items, item{text: listText(l)})
				case indent(l) > 0 && (reUL.MatchString(l) || reOL.MatchString(l)) && len(items) > 0:
					items[len(items)-1].kids = append(items[len(items)-1].kids, listText(l))
				case indent(l) > 0 && len(items) > 0:
					last := &items[len(items)-1]
					if len(last.kids) > 0 {
						last.kids[len(last.kids)-1] += " " + strings.TrimSpace(l)
					} else {
						last.text += " " + strings.TrimSpace(l)
					}
				default:
					goto done
				}
			}
		done:
			i--
			if !ordered && allAnchors(items) {
				continue // a hand-written table of contents: the page has its own
			}
			tag := "ul"
			if ordered {
				tag = "ol"
			}
			out.WriteString("<" + tag + ">\n")
			for _, it := range items {
				out.WriteString("<li>" + inline(it.text))
				if len(it.kids) > 0 {
					out.WriteString("<ul>")
					for _, k := range it.kids {
						out.WriteString("<li>" + inline(k) + "</li>")
					}
					out.WriteString("</ul>")
				}
				out.WriteString("</li>\n")
			}
			out.WriteString("</" + tag + ">\n")
		default:
			para = append(para, t)
		}
	}
	flush()
	return out.String(), toc
}

func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

func sameList(l string, ordered bool) bool {
	if ordered {
		return reOL.MatchString(l)
	}
	return reUL.MatchString(l) && !reOL.MatchString(l)
}

func listText(l string) string {
	if m := reOL.FindStringSubmatch(l); m != nil {
		return m[2]
	}
	return reUL.FindStringSubmatch(l)[2]
}

func allAnchors(items []item) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if len(it.kids) > 0 || !reAnchor.MatchString(strings.TrimSpace(it.text)) {
			return false
		}
	}
	return true
}
