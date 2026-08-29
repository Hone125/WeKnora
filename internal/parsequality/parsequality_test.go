package parsequality

import (
	"math"
	"testing"
)

func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestTextCoverage_Perfect(t *testing.T) {
	if got := TextCoverage("hello world", "hello world"); !floatEq(got, 1.0) {
		t.Fatalf("identical strings should score 1.0, got %v", got)
	}
}

func TestTextCoverage_PartialDrop(t *testing.T) {
	// ref 的 distinct 字符 = {h,e,l,o,' ',w,r,d} 共 8 个，out 只覆盖 h/e/l/o 4 个。
	got := TextCoverage("hello world", "hello")
	if !floatEq(got, 0.5) {
		t.Fatalf("expected 0.5 coverage, got %v", got)
	}
}

func TestTextCoverage_EmptyReference(t *testing.T) {
	if got := TextCoverage("", "anything"); !floatEq(got, 1.0) {
		t.Fatalf("empty reference should be trivially covered, got %v", got)
	}
}

func TestTextSimilarity_WhitespaceInsensitive(t *testing.T) {
	if got := TextSimilarity("Hello  World\n", "hello world"); !floatEq(got, 1.0) {
		t.Fatalf("whitespace/case differences should normalize to 1.0, got %v", got)
	}
}

func TestTextSimilarity_OneEdit(t *testing.T) {
	// "abc" -> "abd": 1 次替换，max=3 → 1 - 1/3。
	got := TextSimilarity("abc", "abd")
	want := 1 - 1.0/3.0
	if !floatEq(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestTextSimilarity_BothEmpty(t *testing.T) {
	if got := TextSimilarity("", ""); !floatEq(got, 1.0) {
		t.Fatalf("two empty strings should score 1.0, got %v", got)
	}
}

func TestStructureFidelity_Perfect(t *testing.T) {
	const doc = "# Title\n- a\n- b\n| x | y |\n"
	if got := StructureFidelity(doc, doc); !floatEq(got, 1.0) {
		t.Fatalf("identical structure should score 1.0, got %v", got)
	}
}

func TestStructureFidelity_Flattened(t *testing.T) {
	// 输出把标题/列表/表格全部拍平成纯文本 → 结构保真度应为 0。
	ref := "# Title\n- a\n- b\n| x | y |\n"
	out := "Title\na\nb\nx y\n"
	got := StructureFidelity(ref, out)
	if !floatEq(got, 0.0) {
		t.Fatalf("fully flattened structure should score 0.0, got %v", got)
	}
}

func TestStructureFidelity_NoStructureEitherSide(t *testing.T) {
	if got := StructureFidelity("plain text", "other plain text"); !floatEq(got, 1.0) {
		t.Fatalf("documents without structure should not be penalised, got %v", got)
	}
}

func TestLevenshtein_Classic(t *testing.T) {
	if d := levenshtein("kitten", "sitting"); d != 3 {
		t.Fatalf("kitten→sitting should be 3 edits, got %d", d)
	}
	if d := levenshtein("", "abc"); d != 3 {
		t.Fatalf("empty→abc should be 3 edits, got %d", d)
	}
}

func TestNormalize_CollapsesWhitespaceAndLowercases(t *testing.T) {
	if got := normalize("  Hello\tWorld\n"); got != "hello world" {
		t.Fatalf("normalize produced %q", got)
	}
}
