// Package parsequality implements deterministic, dependency-free quality
// metrics for comparing a parser engine's Markdown output against a golden
// reference. It is the measurement backbone of M5's cross-engine parsing
// quality baseline: the same document is parsed by several engines and each
// output is scored against one golden Markdown.
//
// All metrics are pure string functions returning a score in [0,1] (1 = best),
// with no cgo/types dependency so the package's tests run cleanly in CI.
package parsequality

import "strings"

// TextCoverage reports how much of the reference's distinct characters are
// present in the output, regardless of order or frequency. It catches content
// that a parser silently dropped (missing text, truncated sections) but is
// insensitive to reordering. A value of 1 means every distinct reference
// character appears somewhere in the output.
func TextCoverage(reference, output string) float64 {
	ref := distinctRunes(normalize(reference))
	if len(ref) == 0 {
		return 1 // empty reference trivially covered
	}
	out := runeSet(normalize(output))
	hit := 0
	for r := range ref {
		if out[r] {
			hit++
		}
	}
	return float64(hit) / float64(len(ref))
}

// TextSimilarity is the normalized Levenshtein similarity between the
// whitespace-normalized reference and output. It is order- and frequency-
// sensitive, so it catches scrambling, duplication and garbled text in
// addition to drops. 1 = identical after whitespace normalization.
func TextSimilarity(reference, output string) float64 {
	a := normalize(reference)
	b := normalize(output)
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	d := levenshtein(a, b)
	max := len(a)
	if len(b) > max {
		max = len(b)
	}
	return 1 - float64(d)/float64(max)
}

// StructureFidelity compares the counts of Markdown structural elements
// (headings, list items, table rows, blockquotes, code fences) between
// reference and output. It catches parsers that flatten headings, drop table
// rows, or merge lists into plain text — the structural degradations most
// damaging to downstream chunking and retrieval.
func StructureFidelity(reference, output string) float64 {
	rc := markdownStructureCounts(reference)
	oc := markdownStructureCounts(output)

	// Element categories in a stable order so the result is deterministic.
	categories := []string{"heading", "list", "table", "quote", "code"}
	total := 0.0
	compared := 0
	for _, c := range categories {
		r := rc[c]
		o := oc[c]
		// Skip categories absent from both (not applicable to this document).
		if r == 0 && o == 0 {
			continue
		}
		denom := r
		if o > denom {
			denom = o
		}
		if denom == 0 {
			denom = 1
		}
		total += 1 - float64(abs(r-o))/float64(denom)
		compared++
	}
	if compared == 0 {
		return 1
	}
	return total / float64(compared)
}

// normalize collapses all whitespace to a single space and lowercases, so
// metrics focus on content rather than formatting differences between engines.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		b.WriteRune(r)
		inSpace = false
	}
	return strings.TrimSpace(strings.ToLower(b.String()))
}

// distinctRunes returns the set of distinct runes in s.
func distinctRunes(s string) map[rune]bool {
	set := make(map[rune]bool)
	for _, r := range s {
		set[r] = true
	}
	return set
}

// runeSet is an alias to make call sites read naturally.
func runeSet(s string) map[rune]bool { return distinctRunes(s) }

// levenshtein returns the Levenshtein edit distance between a and b (runes).
func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}

	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		cur[0] = i
		for j := 1; j <= m; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = min(del, ins, sub)
		}
		prev, cur = cur, prev
	}
	return prev[m]
}

// markdownStructureCounts counts leading-structure markers per line.
func markdownStructureCounts(s string) map[string]int {
	counts := map[string]int{"heading": 0, "list": 0, "table": 0, "quote": 0, "code": 0}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(t, "```"):
			counts["code"]++
		case strings.HasPrefix(t, "#"):
			counts["heading"]++
		case strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "+"):
			counts["list"]++
		case strings.HasPrefix(t, "|"):
			counts["table"]++
		case strings.HasPrefix(t, ">"):
			counts["quote"]++
		}
	}
	return counts
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func min(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
