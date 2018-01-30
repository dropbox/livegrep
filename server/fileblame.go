package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/livegrep/livegrep/blameworthy"
	"github.com/livegrep/livegrep/server/config"
)

// Blame experiment.

type BlameData struct {
	CommitHash     string
	PreviousCommit string
	NextCommit     string
	Author         string
	Date           string
	Subject        string
	Lines          []BlameLine
	Content        string
}

type DiffData struct {
	CommitHash     string
	PreviousCommit string
	NextCommit     string
	Author         string
	Date           string
	Subject        string
	FileDiffs      []DiffFileData
}

type DiffFileData struct {
	Path    string
	Lines   []BlameLine
	Content string
}

type BlameLine struct {
	PreviousCommit     *blameworthy.Commit
	PreviousLineNumber int
	NextCommit         *blameworthy.Commit
	NextLineNumber     int
	OldLineNumber      int
	NewLineNumber      int
	Symbol             string
}

type LogData struct {
	// Only CommitHash, Author, Date, and Subject are filled in
	// for the blame data.
	Blames []BlameData
	// NextOffset is the offset to navigate to in order to
	// paginate forwards. Will be -1 if you can't paginate
	// forwards.
	NextOffset int
	// PrevOffset is the offset to navigate to in order to
	// paginate backwards. Will be -1 if you can't paginate
	// backwards.
	PrevOffset int
}

const (
	diffTimeoutSeconds = 5.0 // most costly file takes about 1.2 seconds
)

var logPaginationLimit = 100

var histories = make(map[string]*blameworthy.GitHistory)
var historiesLock = sync.RWMutex{}

func getHistory(key string) *blameworthy.GitHistory {
	historiesLock.RLock()
	defer historiesLock.RUnlock()
	return histories[key]
}

func setHistory(key string, value *blameworthy.GitHistory) {
	historiesLock.Lock()
	histories[key] = value
	historiesLock.Unlock()
}

func initBlame(cfg *config.Config) error {
	log.Printf("Loading blame...")
	start := time.Now()

	for _, r := range cfg.IndexConfig.Repositories {
		path, ok := r.Metadata["blame"]
		if !ok {
			continue
		}
		var gitLogOutput io.ReadCloser
		if path == "git" {
			var err error
			log.Print("Running git log on: ", r.Path)
			gitLogOutput, err = blameworthy.RunGitLog(r.Path, "HEAD")
			if err != nil {
				log.Print("Skipping blame: ", err)
				continue
			}
		} else {
			var err error
			log.Print("Reading git log file: ", path)
			gitLogOutput, err = os.Open(path)
			if err != nil {
				log.Print("Skipping blame file: ", err)
				continue
			}
		}
		gitHistory, err := blameworthy.ParseGitLog(gitLogOutput)
		if err != nil {
			log.Print("Skipping blame: ", err)
			continue
		}
		setHistory(r.Name, gitHistory)
	}
	elapsed := time.Since(start)
	log.Printf("Blame loaded in %s", elapsed)

	return nil
}

func resolveCommit(repo config.RepoConfig, commitName, path string, data *BlameData) error {
	// TODO: this is an awkward fix for a synchronization problem.
	// The necessary order of operations of a server will be to "git
	// pull" a new master before then running "git log", which means
	// there will be a period of time during which HEAD in the repo
	// has advanced beyond the most recent commit in the blame data.
	// This would cause a 404 when the user lands on the blame page,
	// if we didn't artificially change "HEAD" to the final hash in
	// our list.  Is there some way that we can more organically
	// prevent this problem, that will also work for named branches
	// like "master"?
	if commitName == "HEAD" {
		// "HEAD" -> the last commit we know of.
		h := getHistory(repo.Name).Hashes
		commitName = h[len(h)-1]

		// If we were given a path then pivot, if possible, to
		// the last commit of that file.
		if len(path) > 0 {
			h, ok := getHistory(repo.Name).Files[path]
			if ok {
				commitName = h[len(h)-1].Commit.Hash
			}
		}
	}
	output, err := gitShowCommit(commitName, repo.Path)
	if err != nil {
		return err
	}
	lines := strings.Split(output, "\n")
	data.CommitHash = lines[0][:blameworthy.HashLength]
	data.Author = lines[1]
	data.Date = lines[2]
	data.Subject = lines[3]
	return nil
}

func buildBlameData(
	repo config.RepoConfig,
	commitHash string,
	gitHistory *blameworthy.GitHistory,
	path string,
	isDiff bool,
	data *BlameData,
) error {
	start := time.Now()

	obj := commitHash + ":" + path
	content, err := gitCatBlob(obj, repo.Path)
	if err != nil {
		return err
	}

	lines := []BlameLine{}
	var result *blameworthy.BlameResult

	// Easy enough: simply enumerate the lines of the file.

	result, err = gitHistory.FileBlame(commitHash, path)
	if err != nil {
		return err
	}

	for i, b := range result.BlameVector {
		f := result.FutureVector[i]
		lines = append(lines, BlameLine{
			orBlank(b.Commit),
			b.LineNumber,
			orStillExists(f.Commit),
			f.LineNumber,
			i + 1,
			0,
			"",
		})
	}

	elapsed := time.Since(start)
	log.Print(elapsed, " to prepare blame for ", obj)

	data.PreviousCommit = result.PreviousCommitHash
	data.NextCommit = result.NextCommitHash
	data.Lines = lines
	data.Content = content
	return nil
}

func fileRedirect(gitHistory *blameworthy.GitHistory, repoName, hash, path, dest string) (string, error) {
	j := strings.Index(dest, ".")
	if j == -1 {
		// Redirect to this same file but at another commit.
		url := fmt.Sprint("/blame/", repoName, "/", dest, "/", path, "/")
		return url, nil
	}
	// Otherwise, redirect to a specific file and line in a diff.
	destHash := dest[:j]
	fragment := dest[j+1:]

	var k int
	var diff *blameworthy.Diff
	for k, diff = range gitHistory.Commits[destHash].Diffs {
		if diff.Path == path {
			break
		}
	}

	url := fmt.Sprint("/diff/", repoName, "/", destHash, "/#", k, fragment)
	return url, nil
}

func diffRedirect(w http.ResponseWriter, r *http.Request, repoName string, hash string, rest string) {
	gitHistory := getHistory(repoName)
	if gitHistory == nil {
		http.Error(w, "Repo not configured for blame", 404)
		return
	}
	i := strings.Index(rest, ".")
	if i == -1 {
		url := fmt.Sprint("/diff/", repoName, "/", rest, "/")
		http.Redirect(w, r, url, 307)
		return
	}
	destHash := rest[:i]
	rest = rest[i+1:]
	var j int
	for j = range rest {
		if rest[j] >= 65 {
			break
		}
	}
	if j == len(rest) {
		http.Error(w, "Not found", 404)
		return
	}
	fmt.Print("A\n", rest, " ", rest[:j], "\n")
	commitIndex, err := strconv.Atoi(rest[:j])
	if err != nil {
		http.Error(w, "Not found", 404)
		return
	}
	fmt.Print("A\n")
	path := gitHistory.Commits[hash].Diffs[commitIndex].Path

	var fragment, url string
	fmt.Print(rest[j], "\n")
	if rest[j] == 102 { // "f"
		fragment = rest[j+1:]
		url = fmt.Sprint("/blame/", repoName, "/", destHash,
			"/", path, "/#", fragment)
	} else {
		//path := gitHistory.Commits[hash][commitIndex]
		destIndex := indexOfFileInCommit(gitHistory, path, destHash)
		fragment = rest[j:]
		// TODO: need to turn path into index into that other diff
		url = fmt.Sprint("/diff/", repoName, "/", destHash,
			"/#", destIndex, fragment)
	}

	fmt.Print(url, "\n")
	http.Redirect(w, r, url, 307)
}

func indexOfFileInCommit(history *blameworthy.GitHistory, path string, hash string) int {
	for k, diff := range history.Commits[hash].Diffs {
		if diff.Path == path {
			return k
		}
	}
	return -1
}

func buildDiffData(
	repo config.RepoConfig,
	commitHash string,
	data *DiffData,
) error {
	gitHistory := getHistory(repo.Name)
	if gitHistory == nil {
		return fmt.Errorf("Repo not configured for blame")
	}

	// TODO: turn long hashes into short ones, in case they hand-edit URL?
	commit, ok := gitHistory.Commits[commitHash]
	if !ok {
		return fmt.Errorf("No such commit")
	}

	start := time.Now()
	for _, diff := range commit.Diffs {
		if time.Since(start) > diffTimeoutSeconds*time.Second {
			msg := fmt.Sprintf(`

Alas!

This diff is too big for the alpha version of code.pp git-blame to render
without falling over.

Either search for '%s' in Phabricator's search field or run
this command in a local repository to at least view the diff:

git show %s`, commitHash, commitHash)
			return fmt.Errorf(msg)
		}
		lines, content_lines, err := extendDiff(repo, commitHash, gitHistory, diff.Path)
		if err != nil {
			return err
		}
		data.FileDiffs = append(data.FileDiffs, DiffFileData{
			diff.Path, lines, strings.Join(content_lines, "\n"),
		})
	}

	elapsed := time.Since(start)
	log.Print(elapsed, " to prepare blame for ", commitHash)

	// TODO: add map so this lookup is O(1)?
	var i int
	for i = range gitHistory.Hashes {
		if gitHistory.Hashes[i] == commitHash {
			break
		}
	}
	data.PreviousCommit = ""
	data.NextCommit = ""
	if i-1 >= 0 {
		data.PreviousCommit = gitHistory.Hashes[i-1]
	}
	if i+1 < len(gitHistory.Hashes) {
		data.NextCommit = gitHistory.Hashes[i+1]
	}
	return nil
}

func extendDiff(
	repo config.RepoConfig,
	commitHash string,
	gitHistory *blameworthy.GitHistory,
	path string,
) ([]BlameLine, []string, error) {

	lines := []BlameLine{}
	content_lines := []string{}

	result, err := gitHistory.DiffBlame(commitHash, path)
	if err != nil {
		return lines, content_lines, err
	}
	blameVector := result.BlameVector
	futureVector := result.FutureVector

	new_lines := []string{}
	old_lines := []string{}

	if len(futureVector) > 0 {
		obj := commitHash + ":" + path
		content, err := gitCatBlob(obj, repo.Path)
		if err != nil {
			err = fmt.Errorf("Error getting blob: %s", err)
			return lines, content_lines, err
		}
		new_lines = splitLines(content)
	}

	if len(blameVector) > 0 {
		obj := result.PreviousCommitHash + ":" + path
		content, err := gitCatBlob(obj, repo.Path)
		if err != nil {
			err = fmt.Errorf("Error getting blob: %s", err)
			return lines, content_lines, err
		}
		old_lines = splitLines(content)
	}

	j := 0
	k := 0

	both := func() {
		lines = append(lines, BlameLine{
			orBlank(blameVector[j].Commit),
			blameVector[j].LineNumber,
			orStillExists(futureVector[k].Commit),
			futureVector[k].LineNumber,
			j + 1,
			k + 1,
			"",
		})
		content_lines = append(content_lines, old_lines[j])
		j++
		k++

	}
	left := func() {
		lines = append(lines, BlameLine{
			orBlank(blameVector[j].Commit),
			blameVector[j].LineNumber,
			//"  (this commit) ",
			&blankCommit,
			0,
			j + 1,
			0,
			"-",
		})
		content_lines = append(content_lines, old_lines[j])
		j++
	}
	right := func() {
		lines = append(lines, BlameLine{
			//"  (this commit) ",
			&blankCommit,
			0,
			orStillExists(futureVector[k].Commit),
			futureVector[k].LineNumber,
			0,
			k + 1,
			"+",
		})
		content_lines = append(content_lines, new_lines[k])
		k++
	}
	context := func(distance int) {
		// fmt.Print("DISTANCE ", distance, " ",
		// 	til_line, " ", j+1, "\n")
		if distance > 9 {
			for i := 0; i < 3; i++ {
				both()
				distance--
			}
			for ; distance > 3; distance-- {
				j++
				k++
			}
			for i := 0; i < 3; i++ {
				lines = append(lines, BlameLine{
					&ellipsisCommit,
					0,
					&ellipsisCommit,
					//blankHash,
					0,
					0,
					0,
					"",
				})
				content_lines = append(content_lines, "")
			}
		}
		for ; distance > 0; distance-- {
			both()
		}
	}

	for _, h := range result.Hunks {
		if h.OldLength > 0 {
			context(h.OldStart - (j + 1))
			for m := 0; m < h.OldLength; m++ {
				left()
			}
		}
		if h.NewLength > 0 {
			context(h.NewStart - (k + 1))
			for m := 0; m < h.NewLength; m++ {
				right()
			}
		}
	}
	end := len(old_lines) + 1
	context(end - (j + 1))

	content_lines = append(content_lines, "")

	return lines, content_lines, nil
}

func buildLogData(
	repo config.RepoConfig,
	gitHistory *blameworthy.GitHistory,
	path string,
	offset int) (data LogData, err error) {

	diffs, ok := gitHistory.Files[path]
	if !ok {
		return LogData{}, errors.New("Could not find path in blame")
	}

	// diffs is in chronological order, but we want to return
	// them in reverse chronological order.
	count := 0
	for i := len(diffs) - 1 - offset; i >= 0; i-- {
		if count == logPaginationLimit {
			break
		}
		count++

		// TODO: this struct was really not designed for this case
		blameData := BlameData{}
		commit := diffs[i].Commit

		added := 0
		deleted := 0

		for _, diff := range commit.Diffs {
			for _, hunk := range diff.Hunks {
				deleted += hunk.OldLength
				added += hunk.NewLength
			}
		}

		blameData.Content = ""
		if added > 0 && deleted > 0 {
			blameData.Content = fmt.Sprint("-", deleted, ",+", added)
		} else if added > 0 {
			blameData.Content = fmt.Sprint("+", added)
		} else if deleted > 0 {
			blameData.Content = fmt.Sprint("-", deleted)
		}

		err := resolveCommit(repo, commit.Hash, repo.Path, &blameData)
		if err != nil {
			return LogData{}, err
		}

		blameData.Date = blameData.Date[:10]

		i := strings.Index(blameData.Author, "<")
		j := strings.Index(blameData.Author, ">")
		if i != -1 && j != -1 && i < j {
			blameData.Author = blameData.Author[i+1 : j]
		}
		if len(blameData.Author) > 20 {
			blameData.Author = blameData.Author[:19] + "…"
		}

		data.Blames = append(data.Blames, blameData)
	}

	// Set PrevOffset.
	if offset > 0 {
		// If our current offset is not 0, then we can
		// paginate backwards by "offset - logPaginationLimit"
		// (the current offset minus the number of commits the
		// backwards pagination page will show).
		prevOffset := offset - logPaginationLimit
		// Cannot set previous offset to less than 0.
		if prevOffset < 0 {
			data.PrevOffset = 0
		} else {
			data.PrevOffset = prevOffset
		}
	} else {
		// The current offset is 0, which means we can't
		// paginate backwards.
		data.PrevOffset = -1
	}

	// Set NextOffset.
	if offset+logPaginationLimit >= len(diffs) {
		// Cannot paginate forwards.
		// Convince yourself this is correct with this small example:
		//  - diffs: [a, b, c, d], len: 4
		//  - offset: 2
		//  - logPaginationLimit: 2
		//
		// We will return [b, a], offset + logPaginationLimit
		// is 4, which is >= than len(diffs)
		data.NextOffset = -1
	} else {
		data.NextOffset = offset + logPaginationLimit
	}

	return data, nil
}

func gitShowCommit(commitHash string, repoPath string) (string, error) {
	// git show --pretty="%H%n%an <%ae>%n%ci%n%s" --quiet master master:travisdeps.sh
	out, err := exec.Command(
		"git", "-C", repoPath, "show", "--quiet",
		"--pretty=%H%n%an <%ae>%n%ci%n%s", commitHash,
	).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Make something exactly one column wide.
func col(s string) string {
	if len(s) >= 19 {
		return s
	}
	return fmt.Sprintf("%-19s", s)
}

var (
	blankCommit       = blameworthy.Commit{"", col(""), 0, nil}
	stillExistsCommit = blameworthy.Commit{"", col("(still exists)"), 0, nil}
	ellipsisCommit    = blameworthy.Commit{"", col("    ."), 0, nil}
)

func orBlank(c *blameworthy.Commit) *blameworthy.Commit {
	if c == nil {
		return &blankCommit
	}
	return c
}

func orStillExists(c *blameworthy.Commit) *blameworthy.Commit {
	if c == nil {
		return &stillExistsCommit
	}
	return c
}

func splitLines(s string) []string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}
