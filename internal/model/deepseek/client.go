// Package deepseek configures the shared client for DeepSeek's standard API.
package deepseek

import (
	"fmt"
	"strings"

	"xcode/internal/model/chatcompletions"
)

type Config = chatcompletions.Config
type Client = chatcompletions.Client

func New(config Config) (*Client, error) {
	config.Provider = "deepseek"
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("DEEPSEEK_API_KEY or --model-config is required")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.deepseek.com"
	}
	if config.Model == "" {
		config.Model = "deepseek-flash"
	}
	if config.Thinking == "" {
		config.Thinking = "enabled"
	}
	if config.ReasoningEffort == "" {
		config.ReasoningEffort = "high"
	}
	return chatcompletions.New(config)
}
