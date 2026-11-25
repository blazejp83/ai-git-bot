package ai

import (
	"context"
	"sync"
)

// UsageRecord is one AI request's token usage.
type UsageRecord struct {
	Integration string // AI integration name (kept as a name so history survives deleting it)
	Provider    string
	Model       string
	Bot         string
	Session     string // e.g. "owner/repo#12"
	Round       int    // agent round, 0 for single calls
	Kind        string // "agent", "chat" or "review"
	Usage       TokenUsage
}

// ErrorRecord is one failed AI call.
type ErrorRecord struct {
	Integration string
	Provider    string
	Model       string
	Bot         string
	Session     string
	Round       int
	Kind        string
	Message     string
}

// Recorder stores usage and errors. Implementations must not block for long
// and must never fail the AI call: errors are theirs to log.
type Recorder interface {
	RecordUsage(UsageRecord)
	RecordError(ErrorRecord)
}

type ctxKey int

const (
	ctxBot ctxKey = iota
	ctxSession
	ctxRound
	ctxSink
)

// WithBot tags AI calls made with ctx with the bot's name.
func WithBot(ctx context.Context, bot string) context.Context {
	return context.WithValue(ctx, ctxBot, bot)
}

// WithSession tags AI calls made with ctx with what they work on, e.g. "owner/repo#12".
func WithSession(ctx context.Context, session string) context.Context {
	return context.WithValue(ctx, ctxSession, session)
}

// WithRound tags AI calls made with ctx with the agent round (1-based).
func WithRound(ctx context.Context, round int) context.Context {
	return context.WithValue(ctx, ctxRound, round)
}

func callTags(ctx context.Context) (bot, session string, round int) {
	bot, _ = ctx.Value(ctxBot).(string)
	session, _ = ctx.Value(ctxSession).(string)
	round, _ = ctx.Value(ctxRound).(int)
	return
}

// usageSink collects the usage a provider reports during one client call; a
// call can make several requests (a chunked review).
type usageSink struct {
	mu      sync.Mutex
	entries []sinkEntry
}

type sinkEntry struct {
	model string
	usage TokenUsage
}

func withUsageSink(ctx context.Context) (context.Context, *usageSink) {
	s := &usageSink{}
	return context.WithValue(ctx, ctxSink, s), s
}

// reportUsage is called by providers for every response that carries token
// counts. It does nothing when the call is not being recorded.
func reportUsage(ctx context.Context, model string, u TokenUsage) {
	s, ok := ctx.Value(ctxSink).(*usageSink)
	if !ok {
		return
	}
	s.mu.Lock()
	s.entries = append(s.entries, sinkEntry{model: model, usage: u})
	s.mu.Unlock()
}

// recordingClient records the usage and errors of every call of the client it wraps.
type recordingClient struct {
	inner       Client
	recorder    Recorder
	integration string
	provider    string
	model       string // the integration's default model
}

// NewRecordingClient wraps c so its calls are recorded. A nil recorder returns c.
func NewRecordingClient(c Client, r Recorder, integration, provider, model string) Client {
	if r == nil {
		return c
	}
	return &recordingClient{inner: c, recorder: r, integration: integration, provider: provider, model: model}
}

func (c *recordingClient) SupportsNativeTools() bool { return c.inner.SupportsNativeTools() }

func (c *recordingClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	ctx, sink := withUsageSink(ctx)
	out, err := c.inner.ReviewDiff(ctx, req)
	c.record(ctx, "review", resolveModel(req.ModelOverride, c.model), sink, err)
	return out, err
}

func (c *recordingClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	ctx, sink := withUsageSink(ctx)
	out, err := c.inner.Chat(ctx, history, userMessage, opts)
	c.record(ctx, "chat", resolveModel(opts.ModelOverride, c.model), sink, err)
	return out, err
}

func (c *recordingClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	ctx, sink := withUsageSink(ctx)
	resp, err := c.inner.ChatWithTools(ctx, messages, tools, opts)
	c.record(ctx, "agent", resolveModel(opts.ModelOverride, c.model), sink, err)
	return resp, err
}

// record stores what the call reported. A call that succeeded without any
// usage report (CLI providers) still gets a row with zero tokens, so calls
// are counted.
func (c *recordingClient) record(ctx context.Context, kind, model string, sink *usageSink, err error) {
	bot, session, round := callTags(ctx)
	sink.mu.Lock()
	entries := append([]sinkEntry(nil), sink.entries...)
	sink.mu.Unlock()

	if len(entries) == 0 && err == nil {
		entries = []sinkEntry{{model: model}}
	}
	for _, e := range entries {
		m := e.model
		if m == "" {
			m = model
		}
		c.recorder.RecordUsage(UsageRecord{
			Integration: c.integration, Provider: c.provider, Model: m,
			Bot: bot, Session: session, Round: round, Kind: kind, Usage: e.usage,
		})
	}
	if err != nil {
		c.recorder.RecordError(ErrorRecord{
			Integration: c.integration, Provider: c.provider, Model: model,
			Bot: bot, Session: session, Round: round, Kind: kind, Message: err.Error(),
		})
	}
}
