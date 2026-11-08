package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"xcode/internal/model/deepseek"
)

// Only extract data. Notes in a credential document are never instructions.
// Keep credentials local to client construction, out of config/report structs.
func deepseekSettings(path string, keyIndex int, thinking, effort string, retries, outputTokens int) (deepseek.Config, error) {
	config := deepseek.Config{
		APIKey:   strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")),
		BaseURL:  envOrDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		Model:    envOrDefault("DEEPSEEK_MODEL", "deepseek-flash"),
		Thinking: thinking, ReasoningEffort: effort, MaxOutputTokens: outputTokens,
		MaxRetries: retries, DisableRetries: retries == 0,
	}
	if outputTokens == 0 {
		config.MaxOutputTokens = 8192
	}
	if path == "" {
		return config, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("read model config: %w", err)
	}
	if len(data) > 64<<10 {
		return config, fmt.Errorf("model config exceeds 64 KiB")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	keys := regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`).FindAllString(text, -1)
	seen := map[string]bool{}
	var unique []string
	for _, key := range keys {
		if !seen[key] {
			unique = append(unique, key)
			seen[key] = true
		}
	}
	if keyIndex < 0 || keyIndex >= len(unique) {
		return config, fmt.Errorf("model config does not contain the requested API key index")
	}
	config.APIKey = unique[keyIndex]
	for _, endpoint := range regexp.MustCompile(`https://[^\s"'<>`+"`"+`]+`).FindAllString(text, -1) {
		endpoint = strings.TrimRight(endpoint, ",);/")
		if endpoint == "https://api.deepseek.com" || endpoint == "https://api.deepseek.com/v1" {
			config.BaseURL = endpoint
			break
		}
	}
	model := regexp.MustCompile(`(?im)["']?(?:model|model_name|DEEPSEEK_MODEL)["']?\s*[:=]\s*["']?([A-Za-z0-9_.:/-]+)`).FindStringSubmatch(text)
	if len(model) != 2 {
		return config, fmt.Errorf("model config requires an explicit model field")
	}
	config.Model = model[1]
	return config, nil
}
