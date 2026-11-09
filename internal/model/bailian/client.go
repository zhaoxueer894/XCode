// Package bailian configures the shared Chat Completions client for DashScope.
package bailian

import (
	"fmt"
	"strings"

	"xcode/internal/model/chatcompletions"
)

type Config = chatcompletions.Config
type Client = chatcompletions.Client
type APIError = chatcompletions.APIError

func New(config Config) (*Client, error) {
	config.Provider = "bailian"
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("bailian requires an API key: set bailian-api-key in ~/.xcode/config or env DASHSCOPE_API_KEY")
	}
	if strings.TrimSpace(config.Model) == "" {
		config.Model = "qwen-plus"
	}
	return chatcompletions.New(config)
}
