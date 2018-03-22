package templates

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	texttemplate "text/template"

	"github.com/livegrep/livegrep/blameworthy"
)

var possibleURL = regexp.MustCompile(
	`\bhttps?://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=]+`,
)

func templatePath(f reflect.StructField) string {
	if path := f.Tag.Get("template"); path != "" {
		return path
	}
	return strings.ToLower(f.Name) + ".html"
}

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

func LinkTag(rel string, s string, m map[string]string) template.HTML {
	hash := m[strings.TrimPrefix(s, "/")]
	href := s + "?v=" + hash
	hashBytes, _ := hex.DecodeString(hash)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(hashBytes)
	return template.HTML(`<link rel="` + rel + `" href="` + href + `" integrity="` + integrity + `" />`)
}

func scriptTag(s string, m map[string]string) template.HTML {
	hash := m[strings.TrimPrefix(s, "/")]
	href := s + "?v=" + hash
	hashBytes, _ := hex.DecodeString(hash)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(hashBytes)
	return template.HTML(`<script src="` + href + `" integrity="` + integrity + `"></script>`)

}

func getFuncs() map[string]interface{} {
	return map[string]interface{}{
		"loop":         func(n int) []struct{} { return make([]struct{}, n) },
		"toLineNum":    func(n int) int { return n + 1 },
		"prettyCommit": prettyCommit,
		"linkTag":      LinkTag,
		"scriptTag":    scriptTag,
	}
}

func LoadTemplates(base string, templates interface{}) error {
	v := reflect.ValueOf(templates)
	if v.Kind() != reflect.Ptr {
		panic("Load: Must provide pointer-to-struct")
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		panic("Load: Must provide pointer-to-struct")
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)

		is_html_template := f.Type.AssignableTo(reflect.TypeOf((*template.Template)(nil)))
		is_text_template := f.Type.AssignableTo(reflect.TypeOf((*texttemplate.Template)(nil)))
		if !is_html_template && !is_text_template {
			continue
		}

		p := templatePath(f)
		name := path.Base(p)
		var err error
		var tpl interface{}
		if is_html_template {
			tpl, err = template.New(name).Funcs(getFuncs()).ParseFiles(path.Join(base, p))
		} else {
			tpl, err = texttemplate.New(name).Funcs(getFuncs()).ParseFiles(path.Join(base, p))
		}

		if err != nil {
			return err
		}
		v.Field(i).Set(reflect.ValueOf(tpl))
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

func Load(base string, templates interface{}, assetHashFile string, assetHashMap map[string]string) error {
	if err := LoadTemplates(base, templates); err != nil {
		return err
	}
	if err := LoadAssetHashes(assetHashFile, assetHashMap); err != nil {
		return err
	}
	return nil
}

type reloadHandler struct {
	baseDir       string
	t             interface{}
	assetHashFile string
	assetHashMap  map[string]string
	in            http.Handler
}

func ReloadHandler(base string, templates interface{}, assetHashFile string, assetHashMap map[string]string, h http.Handler) http.Handler {
	return &reloadHandler{base, templates, assetHashFile, assetHashMap, h}
}

func (h *reloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e := Load(h.baseDir, h.t, h.assetHashFile, h.assetHashMap)
	if e != nil {
		log.Printf("loading templates: err=%v", e)
	}
	h.in.ServeHTTP(w, r)
}
