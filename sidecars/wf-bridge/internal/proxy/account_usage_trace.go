package proxy

import (
	"strings"

	"antigravity-wf-assistant/internal/storage"
)

// accountUsageTraceContext adds the account-pool selection to the token usage
// event produced after an upstream stream has finished. It is intentionally
// nil for legacy per-model credentials so their existing trace schema remains
// unchanged.
type accountUsageTraceContext struct {
	accountID string
	model     string
	provider  string
	attempt   int
}

func accountUsageTraceForAttempt(lease *storage.AccountLease, model *storage.CustomModel, provider string, attempt int) *accountUsageTraceContext {
	if lease == nil || strings.TrimSpace(lease.ID) == "" {
		return nil
	}

	modelName := ""
	if model != nil {
		modelName = strings.TrimSpace(model.ExternalModelName)
		if modelName == "" {
			modelName = strings.TrimSpace(model.Name)
		}
		if modelName == "" {
			modelName = strings.TrimSpace(model.DisplayName)
		}
	}

	return &accountUsageTraceContext{
		accountID: strings.TrimSpace(lease.ID),
		model:     modelName,
		provider:  strings.TrimSpace(provider),
		attempt:   attempt,
	}
}

func traceAccountUsage(context *accountUsageTraceContext, fields map[string]any) {
	if context != nil {
		fields["accountId"] = context.accountID
		if context.model != "" {
			fields["model"] = context.model
		}
		if context.provider != "" {
			fields["provider"] = context.provider
		}
		if context.attempt > 0 {
			fields["attempt"] = context.attempt
		}
	}
	trace("usage", fields)
}
