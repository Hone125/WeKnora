package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMeasurementProductionCacheHTTPPath(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		var request OpenAIEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		data := make([]map[string]any, len(request.Input))
		for i := range data {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 2}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	provider, err := NewOpenAIEmbedder("test-key", server.URL, "test-model", 511, 2, "measurement-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := &cacheEmbedder{inner: provider, mem: newMemCache(10)}
	ctx, cold := WithMeasurement(context.Background())
	if _, err := cache.BatchEmbed(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := cold.Snapshot(); got.HTTPAttempts != 1 || got.HTTPInputItems != 2 || got.CacheMisses != 2 {
		t.Fatalf("cold: %+v", got)
	}
	ctx, warm := WithMeasurement(context.Background())
	if _, err := cache.BatchEmbed(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := warm.Snapshot(); got.HTTPAttempts != 0 || got.MemoryHits != 2 {
		t.Fatalf("warm: %+v", got)
	}
	ctx, incremental := WithMeasurement(context.Background())
	if _, err := cache.BatchEmbed(ctx, []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	if got := incremental.Snapshot(); got.HTTPAttempts != 1 || got.HTTPInputItems != 1 || got.MemoryHits != 1 {
		t.Fatalf("incremental: %+v", got)
	}
	if received.Load() != 2 {
		t.Fatalf("actual server requests = %d", received.Load())
	}
}

func TestMeasurementConcurrentAndIsolated(t *testing.T) {
	ctx, m := WithMeasurement(context.Background())
	_, other := WithMeasurement(context.Background())
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() { defer wg.Done(); measure(ctx, func(s *MeasurementSnapshot) { s.HTTPAttempts++ }) }()
	}
	wg.Wait()
	if m.Snapshot().HTTPAttempts != 100 || other.Snapshot().HTTPAttempts != 0 {
		t.Fatal("measurement lost counts or leaked between runs")
	}
}

func TestMeasurementCacheNamespaceIsolation(t *testing.T) {
	provider := &OpenAIEmbedder{modelID: "same-model", dimensions: 2}
	legacy := &cacheEmbedder{inner: provider}
	a := &cacheEmbedder{inner: provider, namespace: "experiment-a"}
	b := &cacheEmbedder{inner: provider, namespace: "experiment-b"}
	if a.cacheKey("same-text") == legacy.cacheKey("same-text") || a.cacheKey("same-text") == b.cacheKey("same-text") {
		t.Fatal("experiment namespace collided with another cache")
	}
	if a.cacheKey("same-text") != a.cacheKey("same-text") {
		t.Fatal("unstable experiment cache key")
	}
}

func TestMeasurementHTTPFailureIsNotSuccess(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	provider, err := NewOpenAIEmbedder("test-key", server.URL, "test-model", 511, 2, "failure-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, m := WithMeasurement(context.Background())
	if _, err := provider.BatchEmbed(ctx, []string{"a"}); err == nil {
		t.Fatal("expected HTTP failure")
	}
	if got := m.Snapshot(); got.HTTPAttempts != 1 || got.HTTPFailures != 1 {
		t.Fatalf("failure: %+v", got)
	}
}
