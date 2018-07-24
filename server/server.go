package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"golang.org/x/net/context"

	"github.com/bmizerany/pat"
	libhoney "github.com/honeycombio/libhoney-go"

	"github.com/livegrep/livegrep/server/config"
	"github.com/livegrep/livegrep/server/log"
	"github.com/livegrep/livegrep/server/reqid"
	"github.com/livegrep/livegrep/server/templates"
)

var serveUrlParseError = fmt.Errorf("failed to parse repo and path from URL")

type page struct {
	Title         string
	ScriptName    string
	ScriptData    interface{}
	IncludeHeader bool
	Data          interface{}
	Config        *config.Config
	AssetHashes   map[string]string
	Nonce         template.HTMLAttr // either `` or ` nonce="..."`
}

type server struct {
	config      *config.Config
	bk          map[string]*Backend
	bkOrder     []string
	repos       map[string]config.RepoConfig
	inner       http.Handler
	Templates   map[string]*template.Template
	OpenSearch  *texttemplate.Template
	AssetHashes map[string]string
	Layout      *template.Template

	honey *libhoney.Builder

	serveFilePathRegex *regexp.Regexp
}

func (s *server) loadTemplates() {
	s.Templates = make(map[string]*template.Template)
	err := templates.LoadTemplates(s.config.DocRoot, s.Templates)
	if err != nil {
		panic(fmt.Sprintf("loading templates: %v", err))
	}

	p := s.config.DocRoot + "/templates/opensearch.xml"
	s.OpenSearch = texttemplate.Must(texttemplate.ParseFiles(p))

	s.AssetHashes = make(map[string]string)
	err = templates.LoadAssetHashes(
		path.Join(s.config.DocRoot, "hashes.txt"),
		s.AssetHashes)
	if err != nil {
		panic(fmt.Sprintf("loading templates: %v", err))
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.inner.ServeHTTP(w, r)
}

func (s *server) ServeRoot(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/search", 303)
}

func (s *server) ServeSearch(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	urls := make(map[string]map[string]string, len(s.bk))
	backends := make([]*Backend, 0, len(s.bk))
	sampleRepo := ""
	for _, bkId := range s.bkOrder {
		bk := s.bk[bkId]
		backends = append(backends, bk)
		bk.I.Lock()
		m := make(map[string]string, len(bk.I.Trees))
		urls[bk.Id] = m
		for _, r := range bk.I.Trees {
			if sampleRepo == "" {
				sampleRepo = r.Name
			}
			m[r.Name] = r.Url
		}
		bk.I.Unlock()
	}

	script_data := &struct {
		RepoUrls           map[string]map[string]string `json:"repo_urls"`
		InternalViewRepos  map[string]config.RepoConfig `json:"internal_view_repos"`
		DefaultSearchRepos []string                     `json:"default_search_repos"`
	}{urls, s.repos, s.config.DefaultSearchRepos}

	s.renderPage(ctx, w, r, "index.html", &page{
		Title:         "code search",
		ScriptName:    "codesearch",
		ScriptData:    script_data,
		IncludeHeader: true,
		Data: struct {
			Backends   []*Backend
			SampleRepo string
		}{
			Backends:   backends,
			SampleRepo: sampleRepo,
		},
	})
}

func (s *server) ServeFile(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	repoName, path, err := getRepoPathFromURL(s.serveFilePathRegex, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	commit := r.URL.Query().Get("commit")
        ffl := r.URL.Query().Get("ffl")
        error := r.URL.Query().Get("error")
	if commit == "" {
		commit = "HEAD"
	}

	if len(s.repos) == 0 {
		http.Error(w, "File browsing not enabled", 404)
		return
	}

	repo, ok := s.repos[repoName]
	if !ok {
		http.Error(w, "No such repo", 404)
		return
	}

        h := getHistory(repo.Name).Hashes
        head := h[len(h)-1]

        if ffl != "" && commit != head && error == "" {
                source_lineno, err := strconv.Atoi(ffl)
                if err != nil {
                        http.Error(w, "Invalid line number", 404)
                        return
                }
                ff_commit, ff_lineno, err := fastForward(repo, path, commit, head, source_lineno)
                if err != nil {
                        log.Printf(ctx, err.Error())
                        http.Error(w, err.Error(), 404)
                        return
                }
		url := fmt.Sprint("/view/", repo.Name, "/", path, "?commit=", ff_commit)
		if ff_commit == head {
		        url += fmt.Sprint("&ffl=", ff_lineno)
		} else {
		        url += fmt.Sprint("#L", ff_lineno, "#ff-error")
		}
                http.Redirect(w, r, url, 307)
                return
        }

	data, err := buildFileData(path, repo, commit)
	if err != nil {
		http.Error(w, fmt.Sprint("500 Error reading file: ", err), 500)
		return
	}

	script_data := &struct {
		RepoInfo config.RepoConfig `json:"repo_info"`
		Commit   string            `json:"commit"`
	}{repo, commit}

	s.renderPage(ctx, w, r, "fileview.html", &page{
		Title:         data.PathSegments[len(data.PathSegments)-1].Name,
		ScriptName:    "fileview",
		ScriptData:    script_data,
		IncludeHeader: false,
		Data:          data,
	})
}

func (s *server) ServeLog(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	repoName := r.URL.Query().Get(":repo")
	path := pat.Tail("/log/:repo/", r.URL.Path)
	offsetStr := r.URL.Query().Get("offset")

	offset := 0
	if offsetStr != "" {
		var err error
		offset, err = strconv.Atoi(offsetStr)
		if err != nil {
			http.Error(w, "Invalid offset", 400)
			return
		}
	}

	repo, ok := s.repos[repoName]
	if !ok {
		http.Error(w, "No such repo", 404)
		return
	}

	gitHistory, ok := histories[repo.Name]
	if !ok {
		http.Error(w, "Repo not configued for log", 404)
		return
	}

	logData, err := buildLogData(repo, gitHistory, path, offset)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	s.renderPageCasual(ctx, w, r, "logfile.html", map[string]interface{}{
		"path":    path,
		"repo":    repo,
		"logData": logData,
	})
}

func (s *server) parseBlameURL(r *http.Request) (string, string, error) {
	if len(s.repos) == 0 {
		return "", "", fmt.Errorf("File browsing not enabled")
	}
	repoName := r.URL.Query().Get(":repo")
	hash := r.URL.Query().Get(":hash")
	return repoName, hash, nil
}

func (s *server) ServeBlame(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	repoName, hash, err := s.parseBlameURL(r)
	if err != nil {
		http.Error(w, fmt.Sprint("404 ", err), 404)
		return
	}

	repo, ok := s.repos[repoName]
	if !ok {
		http.Error(w, "No such repo", 404)
		return
	}

	gitHistory, ok := histories[repo.Name]
	if !ok {
		http.Error(w, "Repo not configured for blame", 404)
		return
	}

	rest := pat.Tail("/blame/:repo/:hash/", r.URL.Path)
	i := strings.LastIndex(rest, "/")
	if i == -1 {
		http.Error(w, "Not found", 404)
		return
	}
	path := rest[:i]
	if i+1 < len(rest) {
		dest := rest[i+1:]
		url, err := fileRedirect(gitHistory, repoName, hash, path, dest)
		if err != nil {
			http.Error(w, "Not found", 404)
			return
		}
		http.Redirect(w, r, url, 307)
		return
	}

	data := BlameData{}
	resolveCommit(repo, hash, path, &data)
	if data.CommitHash != hash {
		pat1 := "/" + hash + "/"
		pat2 := "/" + data.CommitHash + "/"
		destURL := strings.Replace(r.URL.Path, pat1, pat2, 1)
		http.Redirect(w, r, destURL, 307)
		return
	}
	err = buildBlameData(repo, hash, gitHistory, path, &data)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.renderPageCasual(ctx, w, r, "blamefile.html", map[string]interface{}{
		"repo":       repo,
		"path":       path,
		"commitHash": hash,
		"blame":      data,
		"content":    data.Content,
	})
}

func (s *server) ServeDiff(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if len(s.repos) == 0 {
		http.Error(w, "404 Repository browsing not enabled", 404)
		return
	}
	repoName := r.URL.Query().Get(":repo")
	hash := r.URL.Query().Get(":hash")
	repo, ok := s.repos[repoName]
	if !ok {
		http.Error(w, "404 No such repository", 404)
		return
	}
	rest := pat.Tail("/diff/:repo/:hash/", r.URL.Path)
	if len(rest) > 0 && rest != "message" {
		diffRedirect(w, r, repoName, hash, rest)
		return
	}
	data := DiffData{}
	data2 := BlameData{}
	resolveCommit(repo, hash, "", &data2)
	if data2.CommitHash != hash {
		pat1 := "/" + hash + "/"
		pat2 := "/" + data2.CommitHash + "/"
		destURL := strings.Replace(r.URL.Path, pat1, pat2, 1)
		http.Redirect(w, r, destURL, 307)
		return
	}
	data.CommitHash = data2.CommitHash
	data.Author = data2.Author
	data.Date = data2.Date
	data.Subject = data2.Subject
	if len(data2.Body) > 0 {
		data.Body = templates.TurnURLsIntoLinks(data2.Body)
	}

	if rest == "message" {
		s.renderPageCasual(ctx, w, r, "blamemessage.html", map[string]interface{}{
			"commitHash": hash,
			"data":       data,
		})
		return
	}

	err := buildDiffData(repo, hash, &data)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	s.renderPageCasual(ctx, w, r, "blamediff.html", map[string]interface{}{
		"repo":       repo,
		"path":       "NONE",
		"commitHash": hash,
		"blame":      data,
	})
}

func (s *server) ServeAbout(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	s.renderPage(ctx, w, r, "about.html", &page{
		Title:         "about",
		IncludeHeader: true,
	})
}

func (s *server) ServeHelp(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// Help is now shown in the main search page when no search has been entered.
	http.Redirect(w, r, "/search", 303)
}

func (s *server) ServeHealthcheck(w http.ResponseWriter, r *http.Request) {
	// All backends must have (at some point) reported an index age for us to
	// report as healthy.
	// TODO: report as unhealthy if a backend goes down after we've spoken to
	// it.
	for _, bk := range s.bk {
		if bk.I.IndexTime.IsZero() {
			http.Error(w, fmt.Sprintf("unhealthy backend '%s' '%s'\n", bk.Id, bk.Addr), 500)
			return
		}
	}
	io.WriteString(w, "ok\n")
}

type stats struct {
	IndexAge int64 `json:"index_age"`
}

func (s *server) ReloadIndexes(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if err := initBlame(s.config); err != nil {
		message := fmt.Sprint("Error reloading blame data: ", err)
		log.Printf(ctx, message)
		http.Error(w, message, 500)
		return
	}
	http.Error(w, "OK", 200)
}

func (s *server) ServeStats(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// For index age, report the age of the stalest backend's index.
	now := time.Now()
	maxBkAge := time.Duration(-1) * time.Second
	for _, bk := range s.bk {
		if bk.I.IndexTime.IsZero() {
			// backend didn't report index time
			continue
		}
		bkAge := now.Sub(bk.I.IndexTime)
		if bkAge > maxBkAge {
			maxBkAge = bkAge
		}
	}
	replyJSON(ctx, w, 200, &stats{
		IndexAge: int64(maxBkAge / time.Second),
	})
}

func (s *server) requestProtocol(r *http.Request) string {
	if s.config.ReverseProxy {
		if proto := r.Header.Get("X-Real-Proto"); len(proto) > 0 {
			return proto
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (s *server) ServeOpensearch(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	data := &struct {
		BackendName, BaseURL string
	}{
		BaseURL: s.requestProtocol(r) + "://" + r.Host + "/",
	}

	for _, bk := range s.bk {
		if bk.I.Name != "" {
			data.BackendName = bk.I.Name
			break
		}
	}

	templateName := "opensearch.xml"
	w.Header().Set("Content-Type", "application/xml")
	err := s.OpenSearch.ExecuteTemplate(w, templateName, data)
	if err != nil {
		log.Printf(ctx, "Error rendering %s: %s", templateName, err)
		return
	}
}

func (s *server) renderPage(ctx context.Context, w io.Writer, r *http.Request, templateName string, pageData *page) {
	t, ok := s.Templates[templateName]
	if !ok {
		log.Printf(ctx, "Error: no template named %v", templateName)
		return
	}

	pageData.Config = s.config
	pageData.AssetHashes = s.AssetHashes

	nonce := getNonce(r)

	if nonce != "" {
		pageData.Nonce = template.HTMLAttr(fmt.Sprintf(` nonce="%s"`, nonce))
	}

	err := t.ExecuteTemplate(w, templateName, pageData)
	if err != nil {
		log.Printf(ctx, "Error rendering %v: %s", templateName, err)
		return
	}
}

func (s *server) renderPageCasual(ctx context.Context, w io.Writer, r *http.Request, templateName string, data map[string]interface{}) {
	t, ok := s.Templates[templateName]
	if !ok {
		log.Printf(ctx, "Error: no template named %v", templateName)
		return
	}

	data["AssetHashes"] = s.AssetHashes

	nonce := getNonce(r)

	if nonce != "" {
		nonce = fmt.Sprintf(` nonce="%s"`, nonce)
	}
	data["Nonce"] = template.HTMLAttr(nonce)

	err := t.ExecuteTemplate(w, templateName, data)
	if err != nil {
		log.Printf(ctx, "Error rendering %v: %s", templateName, err)
		return
	}
}

func getNonce(r *http.Request) string {
	nonce := r.Header.Get("X-PP-CSP-Nonce")
	if nonce == "" {
		return ""
	}
	// Since we copy this directly into HTML, verify its alphabet.
	// https://www.w3.org/TR/CSP3/#grammardef-base64-value
	ok, _ := regexp.MatchString(`^[-A-Za-z0-9+/_=]+$`, nonce)
	if !ok {
		return ""
	}
	return nonce
}

type reloadHandler struct {
	srv   *server
	inner http.Handler
}

func (h *reloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.srv.loadTemplates()
	h.inner.ServeHTTP(w, r)
}

type handler func(c context.Context, w http.ResponseWriter, r *http.Request)

const RequestTimeout = 8 * time.Second

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	ctx = reqid.NewContext(ctx, reqid.New())
	log.Printf(ctx, "http request: remote=%q method=%q url=%q",
		r.RemoteAddr, r.Method, r.URL)
	h(ctx, w, r)
}

func (s *server) Handler(f func(c context.Context, w http.ResponseWriter, r *http.Request)) http.Handler {
	return handler(f)
}

func New(cfg *config.Config) (http.Handler, error) {
	srv := &server{
		config: cfg,
		bk:     make(map[string]*Backend),
		repos:  make(map[string]config.RepoConfig),
	}
	srv.loadTemplates()

	if err := initBlame(cfg); err != nil {
		ctx := context.Background()
		log.Printf(ctx, "Error: %s", err)
		return nil, err
	}

	if cfg.Honeycomb.WriteKey != "" {
		log.Printf(context.Background(),
			"Enabling honeycomb dataset=%s", cfg.Honeycomb.Dataset)
		srv.honey = libhoney.NewBuilder()
		srv.honey.WriteKey = cfg.Honeycomb.WriteKey
		srv.honey.Dataset = cfg.Honeycomb.Dataset
	}

	for _, bk := range srv.config.Backends {
		be, e := NewBackend(bk.Id, bk.Addr)
		if e != nil {
			return nil, e
		}
		be.Start()
		srv.bk[be.Id] = be
		srv.bkOrder = append(srv.bkOrder, be.Id)
	}

	var repoNames []string
	for _, r := range srv.config.IndexConfig.Repositories {
		srv.repos[r.Name] = r
		repoNames = append(repoNames, r.Name)
	}

	serveFilePathRegex, err := buildRepoRegex(repoNames)
	if err != nil {
		return nil, err
	}
	srv.serveFilePathRegex = serveFilePathRegex

	m := pat.New()
	m.Add("GET", "/log/:repo/", srv.Handler(srv.ServeLog))
	m.Add("GET", "/blame/:repo/:hash/", srv.Handler(srv.ServeBlame))
	m.Add("GET", "/diff/:repo/:hash/", srv.Handler(srv.ServeDiff))
	m.Add("GET", "/debug/healthcheck", http.HandlerFunc(srv.ServeHealthcheck))
	m.Add("GET", "/debug/reload-indexes", srv.Handler(srv.ReloadIndexes))
	m.Add("GET", "/debug/stats", srv.Handler(srv.ServeStats))
	m.Add("GET", "/search/:backend", srv.Handler(srv.ServeSearch))
	m.Add("GET", "/search/", srv.Handler(srv.ServeSearch))
	m.Add("GET", "/view/", srv.Handler(srv.ServeFile))
	m.Add("GET", "/about", srv.Handler(srv.ServeAbout))
	m.Add("GET", "/help", srv.Handler(srv.ServeHelp))
	m.Add("GET", "/opensearch.xml", srv.Handler(srv.ServeOpensearch))
	m.Add("GET", "/", srv.Handler(srv.ServeRoot))

	m.Add("GET", "/api/v1/search/:backend", srv.Handler(srv.ServeAPISearch))
	m.Add("GET", "/api/v1/search/", srv.Handler(srv.ServeAPISearch))

	var h http.Handler = m

	if cfg.Reload {
		h = &reloadHandler{srv, h}
	}

	mux := http.NewServeMux()
	mux.Handle("/assets/", http.FileServer(http.Dir(path.Join(cfg.DocRoot, "htdocs"))))
	mux.Handle("/", h)

	srv.inner = mux

	return srv, nil
}

func buildRepoRegex(repoNames []string) (*regexp.Regexp, error) {
	// Sort in descending order of length so most specific match is selected by regex engine
	sort.Slice(repoNames, func(i, j int) bool {
		return len(repoNames[i]) >= len(repoNames[j])
	})

	// Build regex of form "(repo1|repo2)/(path)"
	var buf bytes.Buffer
	for i, repoName := range repoNames {
		buf.WriteString(regexp.QuoteMeta(repoName))
		if i < len(repoNames)-1 {
			buf.WriteString("|")
		}
	}
	repoRegexAlt := buf.String()
	repoFileRegex, err := regexp.Compile(fmt.Sprintf("(%s)/(.*)", repoRegexAlt))
	if err != nil {
		return nil, fmt.Errorf("failed to create regular expression for URL parsing")
	}

	return repoFileRegex, nil
}

func getRepoPathFromURL(repoRegex *regexp.Regexp, url string) (repo string, path string, err error) {
	matches := repoRegex.FindStringSubmatch(pat.Tail("/view/", url))
	if len(matches) == 0 {
		return "", "", serveUrlParseError
	}

	return matches[1], matches[2], nil
}
