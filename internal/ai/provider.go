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
	Type           string
	DefaultURL     string
	SuggestedModels []string
	RequiresAPIKey bool
}

var Providers = []ProviderMeta{
	{Type: "openai", DefaultURL: "https://api.openai.com", SuggestedModels: []string{"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "o3-mini"}, RequiresAPIKey: true},
	{Type: "anthropic", DefaultURL: "https://api.anthropic.com", SuggestedModels: []string{"claude-sonnet-4-6", "claude-haiku-4-5-20251001", "claude-opus-4-6"}, RequiresAPIKey: true},
	{Type: "ollama", DefaultURL: "http://localhost:11434", RequiresAPIKey: false},
	{Type: "llamacpp", DefaultURL: "http://localhost:8000", RequiresAPIKey: false},
}

// ClientFactory creates and caches AI clients from database integrations.
type ClientFactory struct {
	enc   *encrypt.Service
	mu    sync.RWMutex
	cache map[int64]cachedClient
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
	var name, providerType, apiURL, model, authMethod string
	var apiKey, accessToken, oauthAccountID sql.NullString
	var maxTokens, maxDiffChars, maxDiffChunks, retryChars, thinkingBudget int
	var extendedThinking bool
	var updatedAt string

	err := db.QueryRow(`
		SELECT id, name, provider_type, api_url, api_key, model,
		       max_tokens, max_diff_chars_per_chunk, max_diff_chunks, retry_truncated_chunk_chars,
		       auth_method, access_token, oauth_account_id, extended_thinking, thinking_budget, updated_at
		FROM ai_integrations WHERE id = ?
	`, integrationID).Scan(&id, &name, &providerType, &apiURL, &apiKey, &model,
		&maxTokens, &maxDiffChars, &maxDiffChunks, &retryChars,
		&authMethod, &accessToken, &oauthAccountID, &extendedThinking, &thinkingBudget, &updatedAt)
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

	var client Client
	switch providerType {
	case "openai":
		if authMethod == "oauth" && accessToken.Valid {
			decrypted, _ := f.enc.Decrypt(accessToken.String)
			acctID := ""
			if oauthAccountID.Valid {
				acctID = oauthAccountID.String
			}
			client = NewOpenAIClientWithOAuth(apiURL, decrypted, acctID, cfg)
		} else {
			key := ""
			if apiKey.Valid {
				key, _ = f.enc.Decrypt(apiKey.String)
			}
			client = NewOpenAIClient(apiURL, key, cfg)
		}
	case "anthropic":
		key := ""
		if apiKey.Valid {
			key, _ = f.enc.Decrypt(apiKey.String)
		}
		client = NewAnthropicClient(apiURL, key, cfg)
	case "ollama":
		client = NewOllamaClient(apiURL, cfg)
	case "llamacpp":
		client = NewLlamaCppClient(apiURL, cfg)
	default:
		return nil, fmt.Errorf("unknown AI provider type: %s", providerType)
	}

	f.mu.Lock()
	f.cache[id] = cachedClient{updatedAt: updatedAt, client: client}
	f.mu.Unlock()

	slog.Info("Built new AI client", "integration", name, "provider", providerType)
	return client, nil
}

// Evict removes a cached client.
func (f *ClientFactory) Evict(integrationID int64) {
	f.mu.Lock()
	delete(f.cache, integrationID)
	f.mu.Unlock()
}
