package mutatecore

import (
	"strings"
	"unicode"
)

// PatchLineSimilarity scores one file line against one expected line. It is
// only used for recovery ranking, never for deciding whether to mutate.
func PatchLineSimilarity(actual, expected string) int {
	rawActual := strings.TrimSpace(actual)
	rawExpected := strings.TrimSpace(expected)
	if rawActual == "" || rawExpected == "" {
		return 0
	}
	if IsGenericStructuralLine(rawActual) || IsGenericStructuralLine(rawExpected) {
		if rawActual == rawExpected {
			return 250
		}
		return 0
	}
	actual = strings.ToLower(NormalizeComparableLine(rawActual))
	expected = strings.ToLower(NormalizeComparableLine(rawExpected))
	if actual == "" || expected == "" {
		return 0
	}
	if actual == expected {
		return 10000 + len([]rune(actual))
	}
	if strings.Contains(actual, expected) || strings.Contains(expected, actual) {
		return 5000 + minInt(len([]rune(actual)), len([]rune(expected)))
	}
	if sim := RuneEditSimilarity(actual, expected); sim >= 0.72 {
		return 5500 + int(sim*1000) + minInt(len([]rune(actual)), len([]rune(expected)))
	}
	actualTerms := comparableTerms(actual)
	expectedTerms := comparableTerms(expected)
	if len(actualTerms) == 0 || len(expectedTerms) == 0 {
		return 0
	}
	score := 0
	for term := range expectedTerms {
		if actualTerms[term] {
			score += 100 + len([]rune(term))
			continue
		}
		best := 0.0
		for actualTerm := range actualTerms {
			if s := RuneEditSimilarity(actualTerm, term); s > best {
				best = s
			}
		}
		if best >= 0.72 {
			score += int(float64(80+len([]rune(term))) * best)
		}
	}
	return score
}

func comparableTerms(line string) map[string]bool {
	terms := make(map[string]bool)
	for _, term := range strings.FieldsFunc(line, func(char rune) bool {
		return char != '_' && !unicode.IsLetter(char) && !unicode.IsNumber(char)
	}) {
		if len([]rune(term)) >= 2 {
			terms[term] = true
		}
	}
	return terms
}

func scorePatchWindow(window, expected []string) int {
	if len(expected) == 0 {
		return 0
	}
	total := 0
	strong := 0
	compared := 0
	for i, expectedLine := range expected {
		if strings.TrimSpace(expectedLine) == "" {
			if i < len(window) && strings.TrimSpace(window[i]) == "" {
				total += 50
			}
			continue
		}
		compared++
		if i >= len(window) {
			continue
		}
		score := PatchLineSimilarity(window[i], expectedLine)
		total += score
		if score >= 500 {
			strong++
		}
	}
	if compared == 0 {
		return total
	}
	if compared >= 2 && strong >= 2 {
		total += 800 * strong / compared
	} else if compared >= 2 && strong == 1 {
		total += 120
	}
	return total
}

func tightenPatchWindow(window, expected []string) (from, to int) {
	if len(window) == 0 {
		return 0, 0
	}
	firstStrong := -1
	lastStrong := -1
	limit := len(expected)
	if limit > len(window) {
		limit = len(window)
	}
	for i := 0; i < limit; i++ {
		if strings.TrimSpace(expected[i]) == "" {
			continue
		}
		if PatchLineSimilarity(window[i], expected[i]) >= 500 {
			if firstStrong < 0 {
				firstStrong = i
			}
			lastStrong = i
		}
	}
	if firstStrong < 0 {
		return 0, len(window)
	}
	from = firstStrong
	if from > 0 {
		from--
	}
	to = lastStrong + 1
	if to < len(window) {
		to++
	}
	if to > len(window) {
		to = len(window)
	}
	if to <= from {
		return 0, len(window)
	}
	return from, to
}

func padPatchWindow(fileLen, coreStart, coreEnd int) (start, end int) {
	if fileLen <= 0 {
		return 0, 0
	}
	if coreStart < 0 {
		coreStart = 0
	}
	if coreEnd > fileLen {
		coreEnd = fileLen
	}
	if coreEnd <= coreStart {
		coreStart = 0
		coreEnd = fileLen
		if coreEnd > 8 {
			coreEnd = 8
		}
	}
	const (
		preferPad = 2
		minWindow = 8
		maxWindow = 16
	)
	start = coreStart - preferPad
	if start < 0 {
		start = 0
	}
	end = coreEnd + preferPad
	if end > fileLen {
		end = fileLen
	}
	for end-start < minWindow && end < fileLen {
		end++
	}
	for end-start < minWindow && start > 0 {
		if coreStart-start >= preferPad && end-start >= coreEnd-coreStart+preferPad {
			break
		}
		start--
	}
	if end-start > maxWindow {
		coreLen := coreEnd - coreStart
		if coreLen >= maxWindow {
			start = coreStart
			end = coreStart + maxWindow
			if end > fileLen {
				end = fileLen
				start = end - maxWindow
				if start < 0 {
					start = 0
				}
			}
		} else {
			rightPad := maxWindow - coreLen
			if rightPad > preferPad+2 {
				leftPad := preferPad
				if leftPad > rightPad {
					leftPad = rightPad
				}
				start = coreStart - leftPad
				if start < 0 {
					start = 0
				}
				end = start + maxWindow
				if end > fileLen {
					end = fileLen
					start = end - maxWindow
					if start < 0 {
						start = 0
					}
				}
				if start > coreStart {
					start = coreStart
					end = start + maxWindow
					if end > fileLen {
						end = fileLen
					}
				}
			} else {
				start = coreStart
				end = coreStart + maxWindow
				if end > fileLen {
					end = fileLen
					start = end - maxWindow
					if start < 0 {
						start = 0
					}
				}
			}
		}
	}
	return start, end
}

// ClosestPatchWindow returns a 1-based start line and the padded current
// window for a hunk that did not match.
func ClosestPatchWindow(actual, expected []string) (int, []string) {
	if len(actual) == 0 || len(expected) == 0 {
		return 0, nil
	}
	bestStart := -1
	bestEnd := -1
	bestScore := 0
	bestExpected := expected
	windowLen := len(expected)
	if windowLen < 1 {
		windowLen = 1
	}
	maxStart := len(actual)
	if windowLen <= len(actual) {
		maxStart = len(actual) - windowLen + 1
	}
	for start := 0; start < maxStart; start++ {
		end := start + windowLen
		if end > len(actual) {
			end = len(actual)
		}
		score := scorePatchWindow(actual[start:end], expected)
		if score > bestScore {
			bestScore = score
			bestStart = start
			bestEnd = end
			bestExpected = expected
		}
	}
	for expectedIdx, expectedLine := range expected {
		if strings.TrimSpace(expectedLine) == "" {
			continue
		}
		tailExpected := expected[expectedIdx:]
		for actualIdx, actualLine := range actual {
			lineScore := PatchLineSimilarity(actualLine, expectedLine)
			if lineScore < 500 {
				continue
			}
			end := actualIdx + len(tailExpected)
			if end > len(actual) {
				end = len(actual)
			}
			if end <= actualIdx {
				continue
			}
			score := scorePatchWindow(actual[actualIdx:end], tailExpected) + lineScore/40
			if score > bestScore {
				bestScore = score
				bestStart = actualIdx
				bestEnd = end
				bestExpected = tailExpected
			}
		}
	}
	if bestStart < 0 || bestEnd <= bestStart {
		bestIndex := -1
		lineBest := 0
		for actualIndex, actualLine := range actual {
			for _, expectedLine := range expected {
				if score := PatchLineSimilarity(actualLine, expectedLine); score > lineBest {
					lineBest = score
					bestIndex = actualIndex
				}
			}
		}
		if bestIndex < 0 {
			return 0, nil
		}
		bestStart = bestIndex
		bestEnd = bestIndex + 1
	}
	aligned := actual[bestStart:bestEnd]
	from, to := tightenPatchWindow(aligned, bestExpected)
	coreStart := bestStart + from
	coreEnd := bestStart + to
	if coreEnd <= coreStart {
		coreStart = bestStart
		coreEnd = bestEnd
	}
	start, end := padPatchWindow(len(actual), coreStart, coreEnd)
	return start + 1, append([]string(nil), actual[start:end]...)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
