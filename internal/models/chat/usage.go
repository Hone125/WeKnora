package chat

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// UsageRecorder, when set by the application layer at startup, persists each
// model call's usage as a structured ledger row (M2 成本可观测). It is invoked
// inline on the hot path and must therefore be non-blocking; the container
// wires it to an async write. When nil (e.g. in tests), usage is only logged.
//
// This mirrors chat.LocalImageResolver: the chat package is a leaf dependency
// and must not import the repository layer, so persistence is injected as a
// package-level hook from the composition root.
var UsageRecorder func(ctx context.Context, record *types.ModelUsageRecord)

// logUsage emits the standard "[LLM Usage]" line shared by every Chat
// implementation, then forwards the usage to UsageRecorder (if set). It is a
// no-op when usage is nil so callers can pass through optional usage blocks
// without guarding at each call site.
func logUsage(ctx context.Context, modelID, model string, u *types.TokenUsage) {
	if u == nil {
		return
	}
	purpose, prefixFingerprint := types.LLMCallMetadataFromContext(ctx)
	logger.Infof(ctx,
		"[LLM Usage] model=%s, purpose=%s, prompt_prefix=%s, prompt_tokens=%d, completion_tokens=%d, "+
			"total_tokens=%d, cached_tokens=%d, cache_read_tokens=%d, cache_write_tokens=%d, "+
			"cache_miss_tokens=%d, cache_reported=%t, cache_status=%s%s",
		model, purpose, prefixFingerprint, u.PromptTokens, u.CompletionTokens, u.TotalTokens,
		u.CachedTokens, u.CacheReadTokens, u.CacheWriteTokens, u.CacheMissTokens,
		u.CacheReported, u.CacheStatus, usageAttribution(ctx))

	if UsageRecorder == nil {
		return
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	sessionID, _ := types.SessionIDFromContext(ctx)
	UsageRecorder(ctx, &types.ModelUsageRecord{
		TenantID:         tenantID,
		ModelID:          modelID,
		ModelType:        "chat",
		Purpose:          purpose,
		SessionID:        sessionID,
		PromptTokens:     int64(u.PromptTokens),
		CompletionTokens: int64(u.CompletionTokens),
		TotalTokens:      int64(u.TotalTokens),
		CachedTokens:     int64(u.CachedTokens),
		CacheReadTokens:  int64(u.CacheReadTokens),
		CacheWriteTokens: int64(u.CacheWriteTokens),
		CacheMissTokens:  int64(u.CacheMissTokens),
		CacheStatus:      string(u.CacheStatus),
	})
}

// usageAttribution renders the ", session_id=…, principal=…" suffix that
// attributes a usage line to the session and terminal principal that
// triggered the call. Calls that run outside a session or without a resolved
// principal (document parsing, title generation, background jobs) render an
// empty suffix, keeping their lines byte-identical to before.
func usageAttribution(ctx context.Context) string {
	var b strings.Builder
	if sessionID, ok := types.SessionIDFromContext(ctx); ok && sessionID != "" {
		b.WriteString(", session_id=")
		b.WriteString(sessionID)
	}
	if principal, ok := types.PrincipalFromContext(ctx); ok {
		b.WriteString(", principal=")
		b.WriteString(principal.Type)
		b.WriteString(":")
		b.WriteString(principal.ID)
	}
	return b.String()
}
