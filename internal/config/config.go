package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port          string
	DatabaseURL   string
	EncryptionKey string

	// Prompts
	PromptsDir     string
	DefaultPrompt  string
	AgentPrompt    string
	LocalLLMPrompt string

	// Agent
	AgentEnabled            bool
	AgentMaxFiles           int
	AgentMaxTokens          int
	AgentBranchPrefix       string
	AgentMaxFileContentChar int

	// Agent Validation
	AgentValidationEnabled       bool
	AgentValidationMaxRetries    int
	AgentValidationBuildEnabled  bool
	AgentValidationBuildTimeout  int

	// Session
	SessionSecret string
}

func Load() *Config {
	return &Config{
		Port:          envOr("PORT", "8080"),
		DatabaseURL:   envOr("DATABASE_URL", "sqlite://data/aigitbot.db"),
		EncryptionKey: envOr("APP_ENCRYPTION_KEY", ""),

		PromptsDir:     envOr("PROMPTS_DIR", "prompts"),
		DefaultPrompt:  envOr("PROMPTS_DEFAULT_FILE", "default.md"),
		AgentPrompt:    envOr("PROMPTS_AGENT_FILE", "agent.md"),
		LocalLLMPrompt: envOr("PROMPTS_LOCAL_LLM_FILE", "local-llm.md"),

		AgentEnabled:            envBool("AGENT_ENABLED", true),
		AgentMaxFiles:           envInt("AGENT_MAX_FILES", 20),
		AgentMaxTokens:          envInt("AGENT_MAX_TOKENS", 32768),
		AgentBranchPrefix:       envOr("AGENT_BRANCH_PREFIX", "ai-agent/"),
		AgentMaxFileContentChar: envInt("AGENT_MAX_FILE_CONTENT_CHARS", 100000),

		AgentValidationEnabled:      envBool("AGENT_VALIDATION_ENABLED", true),
		AgentValidationMaxRetries:   envInt("AGENT_VALIDATION_MAX_RETRIES", 3),
		AgentValidationBuildEnabled: envBool("AGENT_VALIDATION_BUILD_ENABLED", false),
		AgentValidationBuildTimeout: envInt("AGENT_VALIDATION_BUILD_TIMEOUT", 300),

		SessionSecret: envOr("SESSION_SECRET", "change-me-in-production-please"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
