// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package strutil

// Levenshtein returns the edit distance between two strings.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)

	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)

	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			if ra[i-1] == rb[j-1] {
				curr[j] = prev[j-1]
			} else {
				curr[j] = 1 + min(prev[j], curr[j-1], prev[j-1])
			}
		}
		prev, curr = curr, prev
	}

	return prev[lb]
}