package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestEvaluationMeasurementRecordRoundTrip(t *testing.T) {
	detail := &types.EvaluationDetail{
		Task:   &types.EvaluationTask{ID: "measurement-roundtrip", TenantID: 1},
		Metric: &types.MetricResult{EmbeddingMeasurement: types.JSON(`{"http_attempts":2,"http_input_items":30,"memory_hits":0,"database_hits":0,"cache_misses":30,"http_failures":0}`)},
	}
	restored := recordToDetail(detailToRecord(detail))
	if restored.Metric == nil {
		t.Fatal("missing metrics after record conversion")
	}
	var counts map[string]int64
	if err := json.Unmarshal(restored.Metric.EmbeddingMeasurement, &counts); err != nil {
		t.Fatal(err)
	}
	if counts["http_attempts"] != 2 || counts["http_input_items"] != 30 {
		t.Fatalf("lost measurement: %v", counts)
	}
	legacy := recordToDetail(&types.EvaluationTaskRecord{Metric: types.JSON(`{"retrieval_metrics":{"recall":0.5}}`)})
	if len(legacy.Metric.EmbeddingMeasurement) != 0 {
		t.Fatal("historical records must not fabricate zero counts")
	}
}
