package embedding

import (
	"context"
	"sync"
)

// Measurement measures the production embedding path, not cache table growth.
// It is opt-in, scoped to a context, and stores no text, credentials or vectors.
type Measurement struct {
	mu     sync.Mutex
	counts MeasurementSnapshot
}

type MeasurementSnapshot struct {
	HTTPAttempts   int64 `json:"http_attempts"`
	HTTPInputItems int64 `json:"http_input_items"`
	HTTPFailures   int64 `json:"http_failures"`
	MemoryHits     int64 `json:"memory_hits"`
	DatabaseHits   int64 `json:"database_hits"`
	CacheMisses    int64 `json:"cache_misses"`
}

type measurementContextKey struct{}

func WithMeasurement(ctx context.Context) (context.Context, *Measurement) {
	m := &Measurement{}
	return context.WithValue(ctx, measurementContextKey{}, m), m
}

func (m *Measurement) Snapshot() MeasurementSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts
}

func measure(ctx context.Context, update func(*MeasurementSnapshot)) {
	m, _ := ctx.Value(measurementContextKey{}).(*Measurement)
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	update(&m.counts)
}
