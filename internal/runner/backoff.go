package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

const (
	maxBackoffRetries = 5
	baseBackoffDelay  = 2 * time.Second
)

// callWithBackoff calls ChatWithTools and retries on temporary rate limits.
// Usage limit errors (hard caps) are NOT retried — they propagate immediately.
func (r *Runner) callWithBackoff(ctx context.Context, messages []ai.ConversationMessage, tools []ai.ToolDef) (*ai.ChatResponse, error) {
	for attempt := 0; attempt <= maxBackoffRetries; attempt++ {
		resp, err := r.aiClient.ChatWithTools(ctx, messages, tools, r.chatOpts())
		if err == nil {
			return resp, nil
		}

		// Hard usage limit — do NOT retry, surface to user immediately
		var usageErr *ai.UsageLimitError
		if errors.As(err, &usageErr) {
			r.reporter.OnUsageLimit(ctx, usageErr)
			return nil, usageErr
		}

		// Temporary rate limit — retry with backoff
		var rateErr *ai.RateLimitError
		if errors.As(err, &rateErr) {
			delay := rateErr.RetryAfter
			if delay == 0 {
				delay = baseBackoffDelay * (1 << attempt)
			}
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}

			r.reporter.OnRateLimit(ctx, delay)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		// Non-retryable error
		return nil, err
	}

	return nil, fmt.Errorf("rate limited after %d retries", maxBackoffRetries)
}
