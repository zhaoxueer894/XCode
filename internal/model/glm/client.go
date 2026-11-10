// Package glm configures the shared client for Zhipu GLM's OpenAI-compatible API.
package glm

import (
	"fmt"
	"strings"

	"xcode/internal/model/chatcompletions"
)

type Config = chatcompletions.Config
type Client = chatcompletions.Client

func New(config Config) (*Client, error) {
	config.Provider = "glm"
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("glm requires an API key: set glm-api-key in ~/.xcode/config or env GLM_API_KEY")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://open.bigmodel.cn/api/paas/v4"
	}
	if config.Model == "" {
		config.Model = "glm-4.6"
	}
	if config.MaxOutputTokens == 0 {
		config.MaxOutputTokens = 8192
	}
	return chatcompletions.New(config)
}
