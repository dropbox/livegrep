package blameworthy

import (
	"fmt"
)

type BlameSegment struct {
	LineCount int
	LineStart int
	Commit    *Commit
}

type BlameSegments []BlameSegment

type BlameLine struct {
	Commit     *Commit
	LineNumber int
}

type BlameVector []BlameLine

type BlameResult struct {
	BlameVector        BlameVector
	FutureVector       BlameVector
	PreviousCommitHash string
	NextCommitHash     string
	Hunks              []Hunk
}

func (history GitHistory) DiffBlame(commitHash string, path string) (*BlameResult, error) {
	commits, ok := history.Files[path]
	if !ok {
		return nil, fmt.Errorf("no such file: %v", path)
	}
	i := 0
	for i = range commits {
		if commits[i].Commit.Hash == commitHash {
			break
		}
	}
	if i == len(commits) {
		return nil, fmt.Errorf("commit %v did not change file %v",
			commitHash, path)
	}
	r := BlameResult{}
	r.Hunks = commits[i].Hunks
	r.BlameVector, r.FutureVector = blame(commits, i+1, -1)
	if i-1 >= 0 {
		r.PreviousCommitHash = commits[i-1].Commit.Hash
	}
	if i+1 < len(commits) {
		r.NextCommitHash = commits[i+1].Commit.Hash
	}
	return &r, nil
}

func (history GitHistory) FileBlame(commitHash string, path string) (*BlameResult, error) {
	fileHistory, i, err := history.FindCommit(commitHash, path)
	if err != nil {
		return nil, err
	}
	i-- // TODO: inline FindCommit so we don't need this
	r := BlameResult{}
	r.BlameVector, r.FutureVector = blame(fileHistory, i+1, 0)
	if fileHistory[i].Commit.Hash == commitHash {
		r.PreviousCommitHash = getHash(fileHistory, i-1)
		r.NextCommitHash = getHash(fileHistory, i+1)
	} else {
		r.PreviousCommitHash = getHash(fileHistory, i)
		r.NextCommitHash = getHash(fileHistory, i+1)
	}
	return &r, nil
}

// Produces BlameVector between two commits for the given path. That is, produce BlameVector for the
// specified target commit as if the specified start commit is the first commit for the path.
// This method further differs from FileBlame in that it does not try to compute previous and next commits.
func (history GitHistory) FileBlameWithStart(start_commit, target_commit, path string) (BlameVector, error) {
	fileHistory, indices, err := history.FindCommits([]string{start_commit, target_commit}, path)
	if err != nil {
		return nil, err
	}
	if indices[0] > indices[1] {
		return nil, fmt.Errorf("%s is later than %s", start_commit, target_commit)
	}

	// We need a reasonably large line count for the initial BlameSegment, so that following diffs
	// can step through without going out of bounds. Instead of computing it exactly, we simply make a
	// crude estimate that's guaranteed to work.
	initial_line_count := 1
	for i := indices[0]; i < indices[1]; i++ {
		for _, hunk := range fileHistory[i].Hunks {
			initial_line_count += hunk.OldStart + hunk.OldLength - 1
		}
	}

	segments := BlameSegments{{initial_line_count, 1, fileHistory[indices[0]-1].Commit}}
	for i := indices[0]; i < indices[1]; i++ {
		segments = fileHistory[i].step(segments)
	}
	return segments.flatten(), nil
}

func (history GitHistory) FindCommits(commitHashes []string, path string) (File, []int, error) {
	fileHistory, ok := history.Files[path]
	if !ok {
		return File{}, nil, fmt.Errorf("no such file: %v", path)
	}

	commitMap := make(map[string]int) // Maps a commit hash to its index into fileHistory
	for _, commitHash := range commitHashes {
		commitMap[commitHash] = -1
	}

	j := 0
	commitsFound := 0
	for i := 0; i < len(history.Hashes) && commitsFound < len(commitHashes); i++ {
		h := history.Hashes[i]
		if j < len(fileHistory) && fileHistory[j].Commit.Hash == h {
			j++
		}
		if _, ok := commitMap[h]; ok {
			commitMap[h] = j
			commitsFound += 1
		}
	}
	indices := make([]int, len(commitHashes))
	for i, commitHash := range commitHashes {
		indices[i] = commitMap[commitHash]
		if indices[i] == -1 {
			return File{}, nil, fmt.Errorf("no such commit: %v", commitHash)
		} else if indices[i] == 0 {
			return File{}, nil, fmt.Errorf("file %s does not exist at commit %s", path, commitHash)
		}
	}
	return fileHistory, indices, nil
}

func (history GitHistory) FindCommit(commitHash string, path string) (File, int, error) {
	fileHistory, indices, err := history.FindCommits([]string{commitHash}, path)
	if err != nil {
		return File{}, -1, err
	}
	return fileHistory, indices[0], nil
}

func blame(history File, end int, bump int) (BlameVector, BlameVector) {
	segments := BlameSegments{}
	var i int
	for i = 0; i < end+bump; i++ {
		commit := history[i]
		segments = commit.step(segments)
	}
	blameVector := segments.flatten()
	for ; i < len(history); i++ {
		commit := history[i]
		segments = commit.step(segments)
	}
	segments = segments.wipe()
	reverse_in_place(history)
	for i--; i > end-1; i-- {
		commit := history[i]
		segments = commit.step(segments)
	}
	reverse_in_place(history)
	futureVector := segments.flatten()
	return blameVector, futureVector
}

// Return the hash of the i'th array member if i is in-bounds, else "".
// This makes the above code slightly less verbose.
func getHash(history File, i int) string {
	if i >= 0 && i < len(history) {
		return history[i].Commit.Hash
	}
	return ""
}

func (diff Diff) step(oldb BlameSegments) BlameSegments {
	newb := BlameSegments{}
	olineno := 1
	nlineno := 1

	oi := 0
	ocount := 0
	if len(oldb) > 0 {
		ocount = oldb[0].LineCount
	}

	ff := func(linecount int) {
		// fmt.Print("ff ", linecount, "\n")
		for linecount > 0 && linecount >= ocount {
			// fmt.Print(linecount, oldb, oi, "\n")
			progress := oldb[oi].LineCount - ocount
			start := oldb[oi].LineStart + progress
			hash := oldb[oi].Commit
			newb = append(newb, BlameSegment{ocount, start, hash})
			nlineno += ocount
			linecount -= ocount
			olineno += ocount
			oi += 1
			ocount = 0
			if oi < len(oldb) {
				ocount = oldb[oi].LineCount
			}
		}
		if linecount > 0 {
			progress := oldb[oi].LineCount - ocount
			start := oldb[oi].LineStart + progress
			commit := oldb[oi].Commit
			newb = append(newb, BlameSegment{linecount, start, commit})
			nlineno += linecount
			ocount -= linecount
			olineno += linecount
		}
	}
	skip := func(linecount int) {
		// fmt.Print("skip ", linecount, ocount, oi, oldb, "\n")
		for linecount > 0 && linecount >= ocount {
			linecount -= ocount
			olineno += ocount
			oi += 1
			ocount = 0
			if oi < len(oldb) {
				ocount = oldb[oi].LineCount
			}
		}
		ocount -= linecount
		olineno += linecount
		// olineno += linecount
		// fmt.Print("skip done")
	}
	add := func(linecount int, commit *Commit) {
		// fmt.Print("add ", linecount, commit_hash, "\n")
		start := nlineno
		newb = append(newb, BlameSegment{linecount, start, commit})
		nlineno += linecount
	}

	for _, h := range diff.Hunks {
		// fmt.Print("HUNK ", h, "\n")
		if h.OldLength > 0 {
			ff(h.OldStart - olineno)
			skip(h.OldLength)
		}
		if h.NewLength > 0 {
			ff(h.NewStart - nlineno)
			add(h.NewLength, diff.Commit)
		}
	}

	for oi < len(oldb) {
		// fmt.Print("Trying to ff", ocount, "\n")
		if ocount > 0 {
			ff(ocount)
		} else {
			oi += 1
			ocount = 0
			if oi < len(oldb) {
				ocount = oldb[oi].LineCount
			}
		}
	}

	return newb
}

func reverse_in_place(diffs File) {
	// Reverse the effect of each hunk.
	for i := range diffs {
		for j := range diffs[i].Hunks {
			h := &diffs[i].Hunks[j]
			h.OldStart, h.NewStart = h.NewStart, h.OldStart
			h.OldLength, h.NewLength = h.NewLength, h.OldLength
		}
	}
}

func (segments BlameSegments) wipe() BlameSegments {
	n := 0
	for _, segment := range segments {
		n += segment.LineCount
	}
	return BlameSegments{{n, 1, nil}}
}

func (segments BlameSegments) flatten() BlameVector {
	v := BlameVector{}
	for _, segment := range segments {
		for i := 0; i < segment.LineCount; i++ {
			n := segment.LineStart + i
			v = append(v, BlameLine{segment.Commit, n})
		}
	}
	return v
}
