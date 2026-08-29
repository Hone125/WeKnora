package agent

import (
	"strings"
	"testing"
	"text/template"
)

func TestWikiGranularityGuidance_RoutesByKey(t *testing.T) {
	cases := map[string]string{
		"focused":    WikiGranularityGuidanceFocused,
		"standard":   WikiGranularityGuidanceStandard,
		"exhaustive": WikiGranularityGuidanceExhaustive,
	}
	for key, want := range cases {
		if got := WikiGranularityGuidance(key); got != want {
			t.Errorf("WikiGranularityGuidance(%q) returned unexpected block", key)
			_ = got
		}
	}
}

func TestWikiGranularityGuidance_UnknownDefaultsToStandard(t *testing.T) {
	unknowns := []string{"", "FOCUSED", "detailed", "minimal", "full", "unknown"}
	for _, k := range unknowns {
		if WikiGranularityGuidance(k) != WikiGranularityGuidanceStandard {
			t.Errorf("WikiGranularityGuidance(%q) should fall back to STANDARD block", k)
		}
	}
}

// Sanity check that the three guidance blocks are meaningfully different.
// A regression (e.g. two constants accidentally pointing at the same string)
// would silently disable the user-facing level control.
func TestWikiGranularityGuidance_BlocksAreDistinct(t *testing.T) {
	blocks := []string{
		WikiGranularityGuidanceFocused,
		WikiGranularityGuidanceStandard,
		WikiGranularityGuidanceExhaustive,
	}
	seen := make(map[string]bool, len(blocks))
	for _, b := range blocks {
		if b == "" {
			t.Error("granularity guidance block must not be empty")
			continue
		}
		if seen[b] {
			t.Error("granularity guidance blocks must be distinct")
		}
		seen[b] = true
	}

	// Each block should name its mode, so the LLM can't silently get the
	// wrong guidance without us noticing in review.
	if !strings.Contains(WikiGranularityGuidanceFocused, "FOCUSED") {
		t.Error("focused block should self-identify")
	}
	if !strings.Contains(WikiGranularityGuidanceStandard, "STANDARD") {
		t.Error("standard block should self-identify")
	}
	if !strings.Contains(WikiGranularityGuidanceExhaustive, "EXHAUSTIVE") {
		t.Error("exhaustive block should self-identify")
	}
}

func renderWikiChunkCitation(t *testing.T, candidateSlugs, chunksXML, lang string) string {
	t.Helper()
	tmpl, err := template.New("cite").Parse(WikiChunkCitationPrompt)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]string{
		"CandidateSlugs": candidateSlugs,
		"ChunksXML":      chunksXML,
		"Language":       lang,
	}); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	return b.String()
}

// TestWikiChunkCitationPrompt_StablePrefixAcrossBatches verifies the property
// that enables provider prefix caching (issue #1687): within one document the
// candidate slugs and the static rules are constant and only the per-batch
// <chunks> block changes, so everything up to <chunks> must be byte-identical
// across batches. If the static rules trailed <chunks> they would be re-billed
// on every batch and this prefix would diverge.
func TestWikiChunkCitationPrompt_StablePrefixAcrossBatches(t *testing.T) {
	const slugs = "entity/acme = Acme Corp\nconcept/rag = Retrieval-Augmented Generation"

	a := renderWikiChunkCitation(t, slugs, `<c id="c001">first batch text</c>`, "English")
	b := renderWikiChunkCitation(t, slugs, `<c id="c099">a completely different second batch</c>`, "English")

	// Match the standalone <chunks> tag line (the per-batch data block), not the
	// inline "<chunks> block" references inside the instructions prose.
	const marker = "\n<chunks>\n"
	ia := strings.Index(a, marker)
	ib := strings.Index(b, marker)
	if ia < 0 || ib < 0 {
		t.Fatalf("rendered prompt missing %q block", marker)
	}

	if a[:ia] != b[:ib] {
		t.Errorf("prompt prefix before <chunks> differs across batches — provider prefix cache will miss.\nA-prefix:\n%s\n---\nB-prefix:\n%s", a[:ia], b[:ib])
	}

	// The static rules and per-document candidate-slug block must live inside
	// that shared prefix, not after the varying chunks.
	for _, must := range []string{"### Primary task", "### JSON Formatting Rules", "\n<candidate_slugs>\n"} {
		if idx := strings.Index(a, must); idx < 0 || idx > ia {
			t.Errorf("%q must appear before <chunks> to be part of the cached prefix (idx=%d, chunks=%d)", must, idx, ia)
		}
	}
}

// TestWikiChunkCitationPrompt_PreservesPlaceholders guards against accidental
// loss of a template field during future reorders.
func TestWikiChunkCitationPrompt_PreservesPlaceholders(t *testing.T) {
	for _, field := range []string{"{{.Language}}", "{{.CandidateSlugs}}", "{{.ChunksXML}}"} {
		if !strings.Contains(WikiChunkCitationPrompt, field) {
			t.Errorf("WikiChunkCitationPrompt lost template field %q", field)
		}
	}
}

func TestWikiPageModifyUserPrompt_HidesInternalChunkHandles(t *testing.T) {
	combined := WikiPageModifySystemPrompt + "\n" + WikiPageModifyUserPrompt
	for _, guidance := range []string{
		"NEVER output them in the page body or summary",
		"Source associations are stored separately by the system",
		"clean Markdown without inline chunk IDs",
	} {
		if !strings.Contains(combined, guidance) {
			t.Errorf("WikiPageModifyUserPrompt missing chunk-handle guidance %q", guidance)
		}
	}

	for _, obsolete := range []string{"Preserve Citations", "followed by an inline citation"} {
		if strings.Contains(combined, obsolete) {
			t.Errorf("WikiPageModifyUserPrompt still contains obsolete inline-citation rule %q", obsolete)
		}
	}
}

func renderWikiPageModify(t *testing.T, sourceContexts, title string) string {
	t.Helper()
	tmpl, err := template.New("modify").Parse(WikiPageModifyUserPrompt)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]string{
		"HasAdditions":         "1",
		"SharedSourceContexts": sourceContexts,
		"PageSlug":             "concept/" + strings.ToLower(title),
		"PageTitle":            title,
		"PageType":             "concept",
		"ExistingContent":      "(New page)",
		"NewContent":           "page-specific chunks for " + title,
		"Language":             "English",
	}); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	return b.String()
}

func TestWikiPageModifyUserPrompt_SharedSourceContextPrecedesPageVariables(t *testing.T) {
	const shared = "<document><title>Same Source</title><context>long shared summary</context></document>"
	a := renderWikiPageModify(t, shared, "Alpha")
	b := renderWikiPageModify(t, shared, "Beta")
	marker := "\n<page_metadata>\n"
	ia, ib := strings.Index(a, marker), strings.Index(b, marker)
	if ia < 0 || ib < 0 {
		t.Fatalf("rendered prompt missing page metadata marker")
	}
	if a[:ia] != b[:ib] {
		t.Fatalf("shared source prefix differs across pages\nA: %s\nB: %s", a[:ia], b[:ib])
	}
	if !strings.Contains(a[:ia], shared) {
		t.Fatalf("shared source context is not part of the cacheable prefix: %s", a[:ia])
	}
}

// renderWikiPrompt renders a wiki prompt template with a string→string data map.
// It is a generalised counterpart to renderWikiChunkCitation / renderWikiPageModify
// for the per-document prompts (summary / knowledge-extract / candidate-slug).
func renderWikiPrompt(t *testing.T, name, tmpl string, data map[string]string) string {
	t.Helper()
	tpl, err := template.New(name).Parse(tmpl)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	return b.String()
}

// TestWikiPrompt_StableInstructionPrefixAcrossDocuments verifies the property
// that enables provider prefix caching across a batch ingest: for the three
// per-document wiki prompts the static <instructions> block precedes the
// per-document <document> data block, so two documents with identical KB-level
// settings (language, granularity) render a byte-identical prefix up to
// <document>. Only the per-document content/slug blocks trail it, letting the
// provider reuse the long instruction prefix for every document after the first.
func TestWikiPrompt_StableInstructionPrefixAcrossDocuments(t *testing.T) {
	const marker = "\n<document>\n"
	cases := []struct {
		name         string
		tmpl         string
		dataA        map[string]string
		dataB        map[string]string
		mustInPrefix string
	}{
		{
			name: "WikiSummaryPrompt",
			tmpl: WikiSummaryPrompt,
			dataA: map[string]string{
				"Content": "doc one content", "ExtractedSlugs": "[[entity/a]]", "Language": "English",
			},
			dataB: map[string]string{
				"Content": "completely different doc two content", "ExtractedSlugs": "[[entity/b]]", "Language": "English",
			},
			mustInPrefix: "10. **Empty content rule**",
		},
		{
			name: "WikiKnowledgeExtractPrompt",
			tmpl: WikiKnowledgeExtractPrompt,
			dataA: map[string]string{
				"Content": "doc one content", "PreviousSlugs": "entity/a", "Language": "English",
			},
			dataB: map[string]string{
				"Content": "completely different doc two content", "PreviousSlugs": "entity/b", "Language": "English",
			},
			mustInPrefix: "### JSON Formatting Rules",
		},
		{
			name: "WikiCandidateSlugPrompt",
			tmpl: WikiCandidateSlugPrompt,
			dataA: map[string]string{
				"Content": "doc one content", "PreviousSlugs": "entity/a", "Language": "English",
				"Granularity": "standard", "GranularityGuidance": WikiGranularityGuidanceStandard,
			},
			dataB: map[string]string{
				"Content": "completely different doc two content", "PreviousSlugs": "entity/b", "Language": "English",
				"Granularity": "standard", "GranularityGuidance": WikiGranularityGuidanceStandard,
			},
			mustInPrefix: "### JSON Formatting Rules",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := renderWikiPrompt(t, tc.name, tc.tmpl, tc.dataA)
			b := renderWikiPrompt(t, tc.name, tc.tmpl, tc.dataB)

			ia := strings.Index(a, marker)
			ib := strings.Index(b, marker)
			if ia < 0 || ib < 0 {
				t.Fatalf("rendered prompt missing %q block", marker)
			}
			if a[:ia] != b[:ib] {
				t.Errorf("instruction prefix before <document> differs across documents — provider prefix cache will miss.\nA-prefix:\n%s\n---\nB-prefix:\n%s", a[:ia], b[:ib])
			}
			// 代表固定规则的字符串必须落在共享前缀内，而非动态 document 之后。
			if idx := strings.Index(a, tc.mustInPrefix); idx < 0 || idx > ia {
				t.Errorf("%q must appear before <document> to be part of the cached prefix (idx=%d, doc=%d)", tc.mustInPrefix, idx, ia)
			}
		})
	}
}

// TestWikiPrompt_PreservesPlaceholders guards against accidental loss of a
// template field during the instruction-first reorder.
func TestWikiPrompt_PreservesPlaceholders(t *testing.T) {
	cases := []struct {
		name   string
		tmpl   string
		fields []string
	}{
		{
			name: "WikiSummaryPrompt",
			tmpl: WikiSummaryPrompt,
			fields: []string{"{{.Content}}", "{{.ExtractedSlugs}}", "{{.Language}}"},
		},
		{
			name: "WikiKnowledgeExtractPrompt",
			tmpl: WikiKnowledgeExtractPrompt,
			fields: []string{"{{.Content}}", "{{.PreviousSlugs}}", "{{.Language}}"},
		},
		{
			name: "WikiCandidateSlugPrompt",
			tmpl: WikiCandidateSlugPrompt,
			fields: []string{"{{.Content}}", "{{.PreviousSlugs}}", "{{.Language}}", "{{.Granularity}}", "{{.GranularityGuidance}}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, field := range tc.fields {
				if !strings.Contains(tc.tmpl, field) {
					t.Errorf("%s lost template field %q", tc.name, field)
				}
			}
		})
	}
}
