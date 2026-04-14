package prompt

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

type Service struct {
	dir   string
	mu    sync.RWMutex
	cache map[string]string
}

func NewService(dir string) *Service {
	return &Service{dir: dir, cache: make(map[string]string)}
}

// GetSystemPrompt loads a prompt by name from the prompts directory.
// Falls back to the default AI system prompt if the file doesn't exist.
func (s *Service) GetSystemPrompt(name string) string {
	if name == "" {
		name = "default"
	}

	s.mu.RLock()
	if cached, ok := s.cache[name]; ok {
		s.mu.RUnlock()
		return cached
	}
	s.mu.RUnlock()

	filename := filepath.Join(s.dir, name+".md")
	data, err := os.ReadFile(filename)
	if err != nil {
		slog.Debug("Prompt file not found, using default", "name", name, "err", err)
		return ai.DefaultSystemPrompt
	}

	prompt := string(data)
	s.mu.Lock()
	s.cache[name] = prompt
	s.mu.Unlock()

	slog.Info("Loaded prompt", "name", name, "file", filename, "chars", len(prompt))
	return prompt
}
