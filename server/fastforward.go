package server

import (
        "errors"
        "fmt"
        "path"
        "strings"

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
        if start >= 1 && length >= 0 && start + length <= len(lines) + 1 {
                return lines[start-1:start-1+length], nil
        }
        return nil, errors.New("Unable to slice file content")
}

func penaltyForDistance(distance int) (int) {
        if distance < 1 {
	        return 0
	} else if distance == 1 {
	        return 1
	} else {
	        return 2
	}
}

func min(a, b int) int {
        if a < b { return a } else { return b }
}
func max(a, b int) int {
        if a > b { return a } else { return b }
}

const (
        SOURCE_CHUNK_MAX_CONTEXT int = 10
)

func analyzeEditAndMapLine(source_lines, target_lines []string, source_lineno int) (int, error) {
        if source_lineno < 1 || source_lineno > len(source_lines) {
	        return 0, errors.New("Line number is out of range")
	}
	if len(target_lines) < 1 {
	        return 0, errors.New("Cannot propagate line number in a deletion")
	}
	if len(source_lines) > 8 {
	        // HAX(jongmin): Constraint the # of source lines we run on to avoid quadratic runtime.
		new_start := max(source_lineno - (SOURCE_CHUNK_MAX_CONTEXT / 2), 1)
		new_end := min(source_lineno + (SOURCE_CHUNK_MAX_CONTEXT / 2), len(source_lines))
		return analyzeEditAndMapLine(source_lines[new_start-1:new_end-1], target_lines, source_lineno - new_start + 1)
	}
	source_chars := strings.Join(source_lines, "\n")
	target_chars := strings.Join(target_lines, "\n")
	var score = make([][]int, len(source_chars))
	var track = make([][]int, len(source_chars))
	for i := range score {
	    score[i] = make([]int, len(target_chars))
	    track[i] = make([]int, len(target_chars))
	}
	// score[i][j] ===> score for mapping source_chars[i] to target_chars[j]
	// track[i][j] ===> where source_chars[i-1] mapped to, for the case of score[i][j]
	penaltyForSkipping := 2
	penaltyForSkippingImportantLine := 10
	i1 := len(strings.Join(source_lines[:source_lineno-1], "\n"))
        i2 := i1 + len(source_lines[source_lineno-1])

	for i := range source_chars {
	        penaltyForSkippingThisChar := penaltyForSkipping
		if i1 <= i && i < i2 {
		        penaltyForSkippingThisChar = penaltyForSkippingImportantLine
		}
	        source_char := source_chars[i]
	        for j := 0; j < len(target_chars); j++ {
		        score[i][j] = -1
			track[i][j] = -1
		}
                if i == 0 {
                        for j := 0; j < len(target_chars); j++ {
                                if source_char == target_chars[j] {
				        score[i][j] = penaltyForDistance(j)
				} else if j == 0 {
				        score[i][j] = penaltyForSkippingThisChar
				}
			}
		} else {
			best_score := -1
			best_predecessor := -1
			k_restart := 0
		        for j := 0; j < len(target_chars); j++ {
			        if source_char == target_chars[j] {
					for k := k_restart; k < j; k++ {
					        if score[i-1][k] < 0 { continue }
						candidate_score := score[i-1][k] + penaltyForDistance(j - k)
					        if best_score == -1 || candidate_score < best_score {
						        best_score = candidate_score
							best_predecessor = k
						}
					}
					k_restart = j
					score[i][j] = best_score
					track[i][j] = best_predecessor
				} else if score[i-1][j] > -1 {
				        score[i][j] = score[i-1][j] + penaltyForSkippingThisChar
					track[i][j] = j
				}
			}
		}
	}
	// Track backwards
	var mapping = make([]int, len(source_chars))
	cursor := 0
	for i := len(source_chars) - 1; i >= 0; i-- {
	        if i == len(source_chars) - 1 {
		        best_score := -1
		        for j := 0; j < len(target_chars); j++ {
			        candidate_score := score[i][j] + penaltyForDistance(len(target_chars) - 1 - j)
			        if score[i][j] >= 0 && (best_score == -1 || best_score > candidate_score) {
				        best_score = candidate_score
					cursor = j
				}
			}
		} else {
		        cursor = track[i+1][cursor]
		}
		mapping[i] = cursor
	}

	var target_line_beginnings = make([]int, len(target_lines) + 1)
	var target_line_histogram = make([]int, len(target_lines))
	target_line_beginnings[0] = 0
	for i, target_line := range target_lines {
	    target_line_beginnings[i+1] = target_line_beginnings[i] + len(target_line) + 1
	    target_line_histogram[i] = 0
	}
        j := 0
	for _, m := range mapping[i1:i2] {
                for ; j < len(target_line_histogram) - 1; j++ {
                        if target_line_beginnings[j] <= m && m < target_line_beginnings[j+1] {
                                break
                        }
                }
		target_line_histogram[j] += 1
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

	// DO THE MAGIC
	//r1 := float64((source_lineno - 1) * len(target_lines)) / float64(len(source_lines))
	//r2 := float64(source_lineno * len(target_lines)) / float64(len(source_lines))
        //return int(r1) + 1, nil
}

func FastForward(repo config.RepoConfig, file, source_commit, target_commit string, source_lineno int) (string, int, error) {
        gitHistory, ok := histories[repo.Name]
        if !ok {
                return "", 0, errors.New("Repo not configured for blame")
        }
	// In the simplest case, a line in the target commit will have the same blame info as the
	// line in question in the source commit.
	blamevectors, err := gitHistory.FileBlameVectorBatch([]string { source_commit, target_commit }, file)
	if err != nil || len(blamevectors) != 2 || blamevectors[0] == nil || blamevectors[1] == nil {
	        return "", 0, fmt.Errorf("unable to obtain blame information for commits")
	}
        if source_lineno < 1 || source_lineno > len(blamevectors[0]) {
                return "", 0, errors.New(fmt.Sprintf("Invalid line number %d in %s", source_lineno, source_commit))
        }
        origin := blamevectors[0][source_lineno-1]
        for i, b := range blamevectors[1] {
                if b.Commit.Hash == origin.Commit.Hash && b.LineNumber == origin.LineNumber {
                        return target_commit, i + 1, nil
                }
        }

        // Either the line has been deleted or the line has mutated. We need to track explicitly.
        // TODO(jongmin): Recurse for now, but this could just be a linear loop, given that all the helper
        // functions are going to be in linear in the # of commits between the source and target anyway.
	fileHistory, indices, err := gitHistory.FindCommitBatch([]string { source_commit, target_commit }, file)
	if err != nil {
	        return "", 0, err
	}
	if len(indices) != 2 {
	        return "", 0, errors.New("Invalid number of results from FindCommitBatch")
	}
	index_source := indices[0] - 1
	index_target := indices[1] - 1
        if index_source + 1 < index_target {
                middle_commit := fileHistory[(index_source + index_target) / 2].Commit.Hash
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
	        // TODO(jongmin): Right now we simply look at the chunk that contains the old line...
		// but if we want to handle things like moves, we should really be doing a full mapping between
		// added and removed chunks, instead of assuming the naive pairing.
                for _, hunk := range fileHistory[index_target].Hunks {
                        if (source_lineno >= hunk.OldStart) && (source_lineno < hunk.OldStart + hunk.OldLength) {
			        if hunk.NewLength == 0 {
				        // The line was deleted, so we cannot propagate anymore.
				        return source_commit, source_lineno, nil
				}
                                // Map line numbers in the chunks...
				source_lines, err := getFileSlice(repo, source_commit, file, hunk.OldStart, hunk.OldLength)
				if err != nil {
				        return "", 0, err
				}
				target_lines, err := getFileSlice(repo, target_commit, file, hunk.NewStart, hunk.NewLength)
				if err != nil {
				        return "", 0, err
				}
				result, err := analyzeEditAndMapLine(source_lines, target_lines, source_lineno - hunk.OldStart + 1)
				if err != nil {
				        return "", 0, err
				}
				return target_commit, result + hunk.NewStart - 1, nil
                        }
                }
        }
	return "", 0, errors.New("Should not reach here")
}
