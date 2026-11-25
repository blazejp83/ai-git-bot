package ai

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

// ProviderMeta holds metadata about an AI provider.
type ProviderMeta struct {
	Type            string
	DefaultURL      string
	SuggestedModels []string
	RequiresAPIKey  bool
}

var Providers = []ProviderMeta{
	{Type: "codex", DefaultURL: "", SuggestedModels: []string{"gpt-5.4", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5.1-codex-mini", "gpt-5.1-codex-max"}, RequiresAPIKey: false},
	{Type: "gemini", DefaultURL: "", SuggestedModels: []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-3-pro-preview", "gemini-3-flash-preview"}, RequiresAPIKey: false},
	{Type: "anthropic", DefaultURL: "https://api.anthropic.com", SuggestedModels: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2", "claude-opus-4-8"}, RequiresAPIKey: true},
	{Type: "ollama", DefaultURL: "http://localhost:11434", RequiresAPIKey: false},
	{Type: "llamacpp", DefaultURL: "http://localhost:8000", RequiresAPIKey: false},
}

// ClientFactory creates and caches AI clients from database integrations.
type ClientFactory struct {
	enc      *encrypt.Service
	mu       sync.RWMutex
	cache    map[int64]cachedClient
	recorder Recorder
}

// SetRecorder makes every client built from now on record its usage and errors.
func (f *ClientFactory) SetRecorder(r Recorder) {
	f.mu.Lock()
	f.recorder = r
	f.cache = make(map[int64]cachedClient)
	f.mu.Unlock()
}

type cachedClient struct {
	updatedAt string
	client    Client
}

func NewClientFactory(enc *encrypt.Service) *ClientFactory {
	return &ClientFactory{
		enc:   enc,
		cache: make(map[int64]cachedClient),
	}
}

// GetClient returns a Client for the given AI integration database row.
func (f *ClientFactory) GetClient(db *sql.DB, integrationID int64) (Client, error) {
	var id int64
	var name, providerType, apiURL, model string
	var apiKey sql.NullString
	var maxTokens, maxDiffChars, maxDiffChunks, retryChars, thinkingBudget int
	var extendedThinking bool
	var updatedAt string

	err := db.QueryRow(`
		SELECT id, name, provider_type, api_url, COALESCE(api_key, ''), model,
		       max_tokens, max_diff_chars_per_chunk, max_diff_chunks, retry_truncated_chunk_chars,
		       extended_thinking, thinking_budget, updated_at
		FROM ai_integrations WHERE id = ?
	`, integrationID).Scan(&id, &name, &providerType, &apiURL, &apiKey, &model,
		&maxTokens, &maxDiffChars, &maxDiffChunks, &retryChars,
		&extendedThinking, &thinkingBudget, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("load ai integration %d: %w", integrationID, err)
	}

	// Check cache
	f.mu.RLock()
	if cached, ok := f.cache[id]; ok && cached.updatedAt == updatedAt {
		f.mu.RUnlock()
		return cached.client, nil
	}
	f.mu.RUnlock()

	cfg := Config{
		Model:                 model,
		MaxTokens:             maxTokens,
		MaxDiffCharsPerChunk:  maxDiffChars,
		MaxDiffChunks:         maxDiffChunks,
		RetryTruncatedChunkCh: retryChars,
		ExtendedThinking:      extendedThinking,
		ThinkingBudget:        thinkingBudget,
	}

	// Decrypt API key if present
	key := ""
	if apiKey.Valid && apiKey.String != "" {
		key, _ = f.enc.Decrypt(apiKey.String)
	}

	client, err := NewClient(providerType, apiURL, key, cfg)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	client = NewRecordingClient(client, f.recorder, name, providerType, model)
	f.cache[id] = cachedClient{updatedAt: updatedAt, client: client}
	f.mu.Unlock()

	slog.Info("Built new AI client", "integration", name, "provider", providerType)
	return client, nil
}

// NewClient builds an uncached, unrecorded client for a provider. The factory
// uses it, and so does the connection test, which must neither touch the
// cache nor count as usage.
func NewClient(providerType, apiURL, key string, cfg Config) (Client, error) {
	switch providerType {
	case "codex", "openai":
		// "openai" is treated as "codex" for backward compatibility
		return NewCodexClient(cfg, key), nil
	case "gemini":
		return NewGeminiClient(cfg, key), nil
	case "anthropic":
		return NewAnthropicClient(apiURL, key, cfg), nil
	case "ollama":
		return NewOllamaClient(apiURL, cfg), nil
	case "llamacpp":
		return NewLlamaCppClient(apiURL, cfg), nil
	default:
		return nil, fmt.Errorf("unknown AI provider type: %s", providerType)
	}
}

// Evict removes a cached client.
func (f *ClientFactory) Evict(integrationID int64) {
	f.mu.Lock()
	delete(f.cache, integrationID)
	f.mu.Unlock()
}
