// Package evalgate implements the CI quality gate for evaluation runs.
//
// It compares a run's metrics against a stored baseline and a per-metric
// regression tolerance. Metrics that improve ("higher is better" for every
// retrieval/generation metric here) are never flagged; a metric is only a
// regression when it drops below baseline - tolerance. This is the engine
// behind M4's "提交降召回改动时 CI 自动报错并指出退化指标".
//
// The package is deliberately dependency-free (no types, no repositories) so
// its tests run without the cgo-heavy gojieba dependency that otherwise crashes
// `go test` for any package that imports types.
package evalgate

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// GateConfig holds the quality-gate configuration: a baseline of expected
// metric values and a per-metric tolerance (the maximum allowed drop before a
// metric is judged a regression). Only metrics listed in Thresholds are gated;
// a metric present in Baseline but absent from Thresholds is reported but never
// blocks.
type GateConfig struct {
	// Baseline maps a metric name to its expected value (e.g. "recall": 0.5).
	Baseline map[string]float64 `json:"baseline"`
	// Thresholds maps a metric name to the maximum allowed drop from baseline
	// before it is considered a regression (e.g. "recall": 0.05 means recall
	// dropping more than 5 percentage points fails the gate).
	Thresholds map[string]float64 `json:"thresholds"`
}

// MetricRegression describes a single metric that failed the gate.
type MetricRegression struct {
	Metric    string  `json:"metric"`    // metric name, e.g. "recall"
	Baseline  float64 `json:"baseline"`  // expected value
	Current   float64 `json:"current"`   // measured value this run
	Delta     float64 `json:"delta"`     // current - baseline (negative = worse)
	Tolerance float64 `json:"tolerance"` // allowed drop before failing
}

// GateReport is the result of judging a run against the gate.
type GateReport struct {
	Passed      bool               `json:"passed"`                // true when no gated metric regressed
	Regressions []MetricRegression `json:"regressions,omitempty"` // gated metrics that regressed beyond tolerance
}

// LoadGateConfig reads a GateConfig from a JSON file.
func LoadGateConfig(path string) (GateConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return GateConfig{}, fmt.Errorf("read gate config: %w", err)
	}
	var cfg GateConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return GateConfig{}, fmt.Errorf("parse gate config: %w", err)
	}
	if len(cfg.Thresholds) == 0 {
		return GateConfig{}, fmt.Errorf("gate config %s has no thresholds; refusing to gate on nothing", path)
	}
	return cfg, nil
}

// JudgeGate compares a run's metrics against the gate and returns a report.
//
// current is a flat map of metric name -> measured value. Only metrics named in
// cfg.Thresholds are gated; if a gated metric is missing from current it is
// skipped (a missing metric is a data problem, not a quality regression).
func JudgeGate(current map[string]float64, cfg GateConfig) GateReport {
	report := GateReport{Passed: true, Regressions: []MetricRegression{}}

	// Iterate in a stable order so the report is deterministic.
	metrics := make([]string, 0, len(cfg.Thresholds))
	for m := range cfg.Thresholds {
		metrics = append(metrics, m)
	}
	sort.Strings(metrics)

	for _, metric := range metrics {
		base, ok := cfg.Baseline[metric]
		if !ok {
			continue // baseline not declared: nothing to compare against
		}
		cur, ok := current[metric]
		if !ok {
			continue // metric not reported this run: skip, not a regression
		}
		tol := cfg.Thresholds[metric]
		delta := cur - base
		if delta < -tol {
			report.Passed = false
			report.Regressions = append(report.Regressions, MetricRegression{
				Metric:    metric,
				Baseline:  base,
				Current:   cur,
				Delta:     delta,
				Tolerance: tol,
			})
		}
	}
	return report
}

// FlattenEvaluationResponse extracts retrieval + generation metrics from a raw
// evaluation API response into a flat map suitable for JudgeGate.
//
// It accepts the JSON produced by GET /api/v1/evaluation?task_id=... and reads
// data.metric.retrieval_metrics and data.metric.generation_metrics. Keeping this
// decoding local (map[string]float64) avoids importing types and thus the
// gojieba cgo dependency.
func FlattenEvaluationResponse(raw []byte) (map[string]float64, error) {
	var payload struct {
		Data struct {
			Metric struct {
				Retrieval  map[string]float64 `json:"retrieval_metrics"`
				Generation map[string]float64 `json:"generation_metrics"`
			} `json:"metric"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse evaluation response: %w", err)
	}
	out := make(map[string]float64, len(payload.Data.Metric.Retrieval)+len(payload.Data.Metric.Generation))
	for k, v := range payload.Data.Metric.Retrieval {
		out[k] = v
	}
	for k, v := range payload.Data.Metric.Generation {
		out[k] = v
	}
	return out, nil
}
