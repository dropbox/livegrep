package templates

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/livegrep/livegrep/blameworthy"
)

var possibleURL = regexp.MustCompile(
	`\bhttps?://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=]+`,
)

func prettyCommit(c *blameworthy.Commit) string {
	if len(c.Author) > 0 && c.Date > 0 {
		return fmt.Sprintf("%04d-%02d-%02d %.8s",
			c.Date/10000, c.Date%10000/100, c.Date%100,
			c.Author)
	}
	return c.Hash + "   " // turn 16 characters into 19
}

func TurnURLsIntoLinks(s string) template.HTML {
	// Instead of using a complex RE that matches only valid URLs,
	// let's match anything vaguely URL-like, then use Go's URL
	// parser to decide whether it's a URL.
	matches := possibleURL.FindAllStringIndex(s, -1)
	i := 0
	h := []string{}
	for _, match := range matches {
		j := match[0]
		k := match[1]
		h = append(h, template.HTMLEscapeString(s[i:j]))
		u := s[j:k]
		_, err := url.Parse(u)
		if err != nil {
			h = append(h, template.HTMLEscapeString(u))
		} else {
			h = append(h, "<a href=\"")
			// should maybe go through "urlescaper" and
			// "attrescaper", but template doesn't export them:
			h = append(h, u)
			h = append(h, "\">")
			h = append(h, template.HTMLEscapeString(u))
			h = append(h, "</a>")
		}
		i = k
	}
	h = append(h, template.HTMLEscapeString(s[i:len(s)]))
	return template.HTML(strings.Join(h, ""))
}

func linkTag(nonce template.HTMLAttr, rel string, s string, m map[string]string) template.HTML {
	hash := m[strings.TrimPrefix(s, "/")]
	href := s + "?v=" + hash
	hashBytes, _ := hex.DecodeString(hash)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(hashBytes)
	return template.HTML(fmt.Sprintf(
		`<link%s rel="%s" href="%s" integrity="%s" />`,
		nonce, rel, href, integrity,
	))
}

func scriptTag(nonce template.HTMLAttr, s string, m map[string]string) template.HTML {
	hash := m[strings.TrimPrefix(s, "/")]
	href := s + "?v=" + hash
	hashBytes, _ := hex.DecodeString(hash)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(hashBytes)
	return template.HTML(fmt.Sprintf(
		`<script%s src="%s" integrity="%s"></script>`,
		nonce, href, integrity,
	))
}

func getFuncs() map[string]interface{} {
	return map[string]interface{}{
		"loop":         func(n int) []struct{} { return make([]struct{}, n) },
		"toLineNum":    func(n int) int { return n + 1 },
		"prettyCommit": prettyCommit,
		"linkTag":      linkTag,
		"scriptTag":    scriptTag,
	}
}

func LoadTemplates(base string, templates map[string]*template.Template) error {
	pattern := base + "/templates/common/*.html"
	common := template.New("").Funcs(getFuncs())
	common = template.Must(common.ParseGlob(pattern))

	pattern = base + "/templates/*.html"
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, path := range paths {
		t := template.Must(common.Clone())
		t = template.Must(t.ParseFiles(path))
		templates[filepath.Base(path)] = t
	}
	return nil
}

func LoadAssetHashes(assetHashFile string, assetHashMap map[string]string) error {
	file, err := os.Open(assetHashFile)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for k := range assetHashMap {
		delete(assetHashMap, k)
	}

	for scanner.Scan() {
		pieces := strings.SplitN(scanner.Text(), "  ", 2)
		hash := pieces[0]
		asset := pieces[1]
		(assetHashMap)[asset] = hash
	}

	return nil
}
