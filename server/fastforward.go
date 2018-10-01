package server

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/livegrep/livegrep/blameworthy"
	"github.com/livegrep/livegrep/server/config"
)

func getFileSlice(repo config.RepoConfig, commit, file string, start, length int) ([]string, error) {
	obj := commit + ":" + path.Clean(file)
	objectType, err := gitObjectType(obj, repo.Path)
	if err != nil {
		return nil, err
	}
	if objectType != "blob" {
		return nil, errors.New("gitObjectType failed")
	}
	content, err := gitCatBlob(obj, repo.Path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(content, "\n")
	if start >= 1 && length >= 0 && start+length <= len(lines)+1 {
		return lines[start-1 : start-1+length], nil
	}
	return nil, errors.New("Unable to slice file content")
}

// NOTE: The shape of this penalty function is important for an optimization in the dynamic
// programming below to work.
func penaltyForDistance(distance int) int {
	if distance < 1 {
		return 0
	} else if distance == 1 {
		return 1
	} else {
		return 2
	}
}

func max(a, b int) int {
	if a > b {
		return a
	} else {
		return b
	}
}

const (
	SOURCE_CHUNK_MAX_CONTEXT int = 10
	PENALTY_FOR_SKIPPING     int = 2
)

func analyzeEditAndMapLine(source_lines, target_lines []string, source_lineno int) (int, error) {
	if source_lineno < 1 || source_lineno > len(source_lines) {
		return 0, fmt.Errorf("Line number %d is out of range", source_lineno)
	}
	if len(target_lines) < 1 {
		return 0, errors.New("Cannot propagate line number in a deletion")
	}
	if len(source_lines) > SOURCE_CHUNK_MAX_CONTEXT {
		// HAX(jongmin): Constraint the # of source lines we run on to avoid quadratic runtime.
		new_start := max(source_lineno-(SOURCE_CHUNK_MAX_CONTEXT/2), 1)
		new_end := new_start + SOURCE_CHUNK_MAX_CONTEXT - 1
		if new_end > len(source_lines) {
			new_start = max(len(source_lines)-SOURCE_CHUNK_MAX_CONTEXT+1, 1)
			new_end = len(source_lines)
		}
		new_source_lineno := source_lineno - new_start + 1
		return analyzeEditAndMapLine(source_lines[new_start-1:new_end], target_lines, new_source_lineno)
	}
	source_chars := strings.Join(source_lines, "\n")
	target_chars := strings.Join(target_lines, "\n")

	// The following block of code solves a dynamic programming problem that finds a mapping
	// f that maps each character in source_chars to a character in the target_chars, with the
	// following constraint:
	//   * f: [0..len(source_chars)) ---> [0..len(target_chars)).
	//   * f is weakly monotonic.
	//   * If f(i)=j, then either source_chars[i] == target_chars[j], i.e. the characters match;
	//     Or f(i-1)=j.
	// Basically f tries to figure out where each character in the source ended up in the
	// target chunk. If there is no match for a particular character, the fallback value is the
	// value for the previous character.
	//
	// The score for a particular f is determined by looking at where the characters map to.
	// Generally speaking, we expect consecutive characters in the source to map to consecutive
	// characters in the target, so we penalize when this expectation is not met, i.e.
	// when we're unable to find a match for a character, or when we have to skip ahead to find
	// a match.
	//
	// score[i][j] ===> best score for the restriction f: [0..i] --> [0..len(target(chars))
	//		    where f[i]=j.
	// track[i][j] ===> the value of f[i-1] for f that attains the score of source[i][j]
	var score = make([][]int, len(source_chars))
	var track = make([][]int, len(source_chars))
	for i := range score {
		score[i] = make([]int, len(target_chars))
		track[i] = make([]int, len(target_chars))
	}

	i1 := 0
	for _, line := range source_lines[:source_lineno-1] {
		i1 += len(line) + 1
	}
	i2 := i1 + len(source_lines[source_lineno-1])

	for i := range source_chars {
		source_char := source_chars[i]
		for j := 0; j < len(target_chars); j++ {
			score[i][j] = -1
			track[i][j] = -1
		}

		// Handle the base case
		if i == 0 {
			for j := 0; j < len(target_chars); j++ {
				if source_char == target_chars[j] {
					score[i][j] = penaltyForDistance(j - i)
				} else if j == 0 {
					score[i][j] = PENALTY_FOR_SKIPPING
				}
			}
			continue
		}

		best_score := -1
		best_predecessor := -1
		last_j_with_match := 0
		for j := 0; j < len(target_chars); j++ {
			if source_char == target_chars[j] {
				for k := last_j_with_match; k < j; k++ {
					if score[i-1][k] < 0 {
						continue
					}
					candidate_score := score[i-1][k] + penaltyForDistance(j-k)
					if best_score == -1 || candidate_score <= best_score {
						best_score = candidate_score
						best_predecessor = k
					}
				}
				// Reusing best_score/best_predecessor across different values of j
				// is not quite correct; the penaltyForDistance(...) term can be off
				// by one, so we correct it here. Note that correcting is only safe
				// because the amount of correction is at most by +1.
				if -1 < best_predecessor && best_predecessor < last_j_with_match {
					best_score -= penaltyForDistance(last_j_with_match - best_predecessor)
					best_score += penaltyForDistance(j - best_predecessor)
				}
				score[i][j] = best_score
				track[i][j] = best_predecessor
				last_j_with_match = j
			} else if score[i-1][j] > -1 {
				score[i][j] = score[i-1][j] + PENALTY_FOR_SKIPPING
				track[i][j] = j
			}
		}
	}

	// Track backwards to reconstruct the mapping function f.
	var mapping = make([]int, len(source_chars))
	cursor := -1
	for i := len(source_chars) - 1; i >= 0; i-- {
		if i == len(source_chars)-1 {
			best_score := -1
			for j := 0; j < len(target_chars); j++ {
				candidate_score := score[i][j] + penaltyForDistance(len(target_chars)-1-j)
				if score[i][j] >= 0 && (best_score == -1 || best_score > candidate_score) {
					best_score = candidate_score
					cursor = j
				}
			}
		} else {
			if cursor != -1 {
				cursor = track[i+1][cursor]
			}
		}
		mapping[i] = cursor
	}

	// Compute where the characters in the source line being tracked ended up.
	var target_line_beginnings = make([]int, len(target_lines)+1)
	var target_line_histogram = make([]int, len(target_lines))
	target_line_beginnings[0] = 0
	for i, target_line := range target_lines {
		target_line_beginnings[i+1] = target_line_beginnings[i] + len(target_line) + 1
		target_line_histogram[i] = 0
	}
	j := 0
	prev_mapping := -1
	for _, m := range mapping[i1:i2] {
		for ; j < len(target_line_histogram)-1; j++ {
			if target_line_beginnings[j] <= m && m < target_line_beginnings[j+1] {
				break
			}
		}
		if prev_mapping != m {
			target_line_histogram[j] += 1
		}
		prev_mapping = m
	}
	best_score := 0
	best_target_line := 0
	for i := range target_line_histogram {
		if best_score < target_line_histogram[i] {
			best_score = target_line_histogram[i]
			best_target_line = i
		}
	}
	return best_target_line + 1, nil
}

func FastForward(repo config.RepoConfig, file, source_commit, target_commit string, source_lineno int) (string, int, error) {
	gitHistory, ok := histories[repo.Name]
	if !ok {
		return "", 0, errors.New("Repo not configured for blame")
	}
	if source_lineno < 1 {
		return "", 0, errors.New(fmt.Sprintf("Invalid line number %d in %s", source_lineno, source_commit))
	}

	// In the simplest case, a line in the target commit will have the same blame info as the
	// line in question in the source commit.
	blamevector, err := gitHistory.FileBlameWithStart(source_commit, target_commit, file)
	if err != nil {
		return "", 0, err
	}
	if blamevector == nil {
		return "", 0, fmt.Errorf("unable to obtain blame information for commits")
	}
	for i, b := range blamevector {
		if b.Commit.Hash == source_commit && b.LineNumber == source_lineno {
			return target_commit, i + 1, nil
		}
	}

	// Either the line has been deleted or the line has mutated. We need to track explicitly.
	// TODO: Recurse for now, but this could just be a linear loop, given that all the helper
	// functions are going to be in linear in the # of commits between the source and target anyway.
	fileHistory, indices, err := gitHistory.FindCommits([]string{source_commit, target_commit}, file)
	if err != nil {
		return "", 0, err
	}
	index_source := indices[0] - 1
	index_target := indices[1] - 1
	if index_source+1 < index_target {
		middle_commit := fileHistory[(index_source+index_target)/2].Commit.Hash
		commit, middle_lineno, err := FastForward(repo, file, source_commit, middle_commit, source_lineno)
		if err != nil {
			return "", 0, err
		}
		if commit != middle_commit {
			// We were unable to fully propagate the line number, so bail.
			return commit, middle_lineno, nil
		}
		return FastForward(repo, file, middle_commit, target_commit, middle_lineno)
	} else {
		// TODO: Right now we simply look at the chunk that contains the old line, but if
		// we want to handle things like moves, we should really be doing a full mapping
		// between added and removed chunks, instead of assuming the naive pairing.
		var hunk blameworthy.Hunk
		for _, hunk = range fileHistory[index_target].Hunks {
			if (source_lineno >= hunk.OldStart) && (source_lineno < hunk.OldStart+hunk.OldLength) {
				break
			}
		}
		if hunk.NewLength == 0 {
			// The line was deleted, so we cannot propagate anymore.
			return source_commit, source_lineno, nil
		}
		// Map line numbers in the chunks.
		source_lines, err := getFileSlice(repo, source_commit, file, hunk.OldStart, hunk.OldLength)
		if err != nil {
			return "", 0, err
		}
		target_lines, err := getFileSlice(repo, target_commit, file, hunk.NewStart, hunk.NewLength)
		if err != nil {
			return "", 0, err
		}
		result, err := analyzeEditAndMapLine(source_lines, target_lines, source_lineno-hunk.OldStart+1)
		if err != nil {
			return "", 0, err
		}
		return target_commit, result + hunk.NewStart - 1, nil
	}
}
