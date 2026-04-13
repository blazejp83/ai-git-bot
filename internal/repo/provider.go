package repo

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

// ProviderMeta holds metadata about a git provider.
type ProviderMeta struct {
	Type       string
	DefaultURL string
}

var Providers = []ProviderMeta{
	{Type: "GITEA", DefaultURL: "https://gitea.example.com"},
	{Type: "GITHUB", DefaultURL: "https://api.github.com"},
	{Type: "GITLAB", DefaultURL: "https://gitlab.com"},
	{Type: "BITBUCKET", DefaultURL: "https://api.bitbucket.org/2.0"},
}

// ClientFactory creates and caches repo clients from database integrations.
type ClientFactory struct {
	enc   *encrypt.Service
	mu    sync.RWMutex
	cache map[int64]cachedRepoClient
}

type cachedRepoClient struct {
	updatedAt string
	client    Client
}

func NewClientFactory(enc *encrypt.Service) *ClientFactory {
	return &ClientFactory{
		enc:   enc,
		cache: make(map[int64]cachedRepoClient),
	}
}

func (f *ClientFactory) GetClient(db *sql.DB, integrationID int64) (Client, error) {
	var id int64
	var name, providerType, apiURL, token string
	var username sql.NullString
	var updatedAt string

	err := db.QueryRow(`
		SELECT id, name, provider_type, url, username, token, updated_at
		FROM git_integrations WHERE id = ?
	`, integrationID).Scan(&id, &name, &providerType, &apiURL, &username, &token, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("load git integration %d: %w", integrationID, err)
	}

	f.mu.RLock()
	if cached, ok := f.cache[id]; ok && cached.updatedAt == updatedAt {
		f.mu.RUnlock()
		return cached.client, nil
	}
	f.mu.RUnlock()

	decryptedToken, _ := f.enc.Decrypt(token)
	creds := Credentials{
		BaseURL:  apiURL,
		CloneURL: apiURL,
		Token:    decryptedToken,
	}
	if username.Valid {
		creds.Username = username.String
	}

	var client Client
	switch providerType {
	case "GITEA":
		client = NewGiteaClient(creds)
	case "GITHUB":
		client = NewGitHubClient(creds)
	case "GITLAB":
		client = NewGitLabClient(creds)
	case "BITBUCKET":
		client = NewBitbucketClient(creds)
	default:
		return nil, fmt.Errorf("unknown git provider type: %s", providerType)
	}

	f.mu.Lock()
	f.cache[id] = cachedRepoClient{updatedAt: updatedAt, client: client}
	f.mu.Unlock()

	slog.Info("Built new repo client", "integration", name, "provider", providerType)
	return client, nil
}
