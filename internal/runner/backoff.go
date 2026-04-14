package runner

import (
	"context"
	"errors"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

const (
	maxBackoffRetries = 5
	baseBackoffDelay  = 2 * time.Second
)

// callWithBackoff calls ChatWithTools and retries on rate limit errors
// with exponential backoff.
func (r *Runner) callWithBackoff(ctx context.Context, messages []ai.ConversationMessage, tools []ai.ToolDef) (*ai.ChatResponse, error) {
	for attempt := 0; attempt <= maxBackoffRetries; attempt++ {
		resp, err := r.aiClient.ChatWithTools(ctx, messages, tools, r.chatOpts())
		if err == nil {
			return resp, nil
		}

		var rateErr *ai.RateLimitError
		if errors.As(err, &rateErr) {
			delay := rateErr.RetryAfter
			if delay == 0 {
				delay = baseBackoffDelay * (1 << attempt) // exponential: 2s, 4s, 8s, 16s, 32s
			}
			// Cap at 60 seconds
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

	return nil, &ai.RateLimitError{
		StatusCode: 429,
		Body:       "rate limited after max retries",
	}
}
