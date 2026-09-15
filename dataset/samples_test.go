package main

import (
	"reflect"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// Verify the actual files sent to the evaluation pipeline match the public,
// synthetic fixtures in this package, not unrelated private document contents.
func TestSamplesMatchPublicGenerator(t *testing.T) {
	corpus, err := parquet.ReadFile[TextInfo]("samples/corpus.parquet")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(corpus, passages) {
		t.Fatal("corpus differs from the public synthetic generator")
	}
	queries, err := parquet.ReadFile[TextInfo]("samples/queries.parquet")
	if err != nil {
		t.Fatal(err)
	}
	answers, err := parquet.ReadFile[TextInfo]("samples/answers.parquet")
	if err != nil {
		t.Fatal(err)
	}
	qrels, err := parquet.ReadFile[RelsInfo]("samples/qrels.parquet")
	if err != nil {
		t.Fatal(err)
	}
	qas, err := parquet.ReadFile[QaInfo]("samples/qas.parquet")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != len(qaItems) || len(answers) != len(qaItems) || len(qrels) != len(qaItems) || len(qas) != len(qaItems) {
		t.Fatal("sample size differs from public generator")
	}
	for i, q := range qaItems {
		if queries[i] != (TextInfo{ID: q.id, Text: q.question}) || answers[i] != (TextInfo{ID: q.id, Text: q.answer}) || qrels[i] != (RelsInfo{QID: q.id, PID: q.pid}) || qas[i] != (QaInfo{QID: q.id, AID: q.id}) {
			t.Fatalf("sample %d differs from the public synthetic generator", i)
		}
	}
}
