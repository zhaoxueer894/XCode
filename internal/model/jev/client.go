// Package jev 实现 TypeSafe Jev "System One" API 的最小 HTTP 客户端：
// 一次请求携带 state（程序状态文本）与一组类型化问题，返回带校准概率的类型化决策。
// 接入边界：按第三方文档（dev.to 实用指南）实现端点与报文形状，
// 未经真实调用核验前不声称与官方 API 完全一致；第一次连通性验证用于确认并修正形状。
// 本包不保存密钥：Key 由调用方从环境注入，也不得进入日志。
package jev

import (
	"bytes"         // 请求体缓冲。
	"context"       // 请求取消与超时。
	"encoding/json" // 请求/响应编码。
	"fmt"           // 构造错误。
	"io"            // 限制并读取响应体。
	"net/http"      // HTTP 客户端。
	"strings"       // 响应头校验。
	"time"          // 默认超时。
)

// DefaultEndpoint 与 DefaultModel 来自 TypeSafe 公开材料（dev.to 实用指南）；
// 官方文档核实后如有出入，只需改这两个常量。
const (
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-latest"
)

// maxResponseBytes 限制响应体大小，防止异常服务端拖垮进程。
const maxResponseBytes = 8 << 20

// QuestionType 表示 Jev 支持的类型化问题种类；本客户端只用到 Choice。
type QuestionType string

const (
	QuestionChoice QuestionType = "choice" // 从 criteria 选项中选一个，返回选择 + 概率 + 置信度。
)

// Question 保存一个类型化问题；Choice 型带判定说明与各选项的判据。
type Question struct {
	Type         QuestionType      `json:"type"`               // 保存问题类型；当前固定 choice。
	Instructions string            `json:"instructions"`       // 保存判定说明（给模型的规则）。
	Criteria     map[string]string `json:"criteria,omitempty"` // 保存各选项的判据文本；键即选项名。
}

// answerRaw 保存响应中单个问题的原始答案字段；字段名以第一次连通性验证为准。
type answerRaw struct {
	Choice        string             `json:"choice"`                  // Choice 型问题选中的选项名。
	Probabilities map[string]float64 `json:"probabilities,omitempty"` // 保存各选项的校准概率。
	Confidence    float64            `json:"confidence"`              // 保存整体置信度 0-1。
}

// response 保存 systemone 的响应体；answers 以问题 ID 为键。
type response struct {
	Model   string               `json:"model,omitempty"`   // 保存实际应答的模型版本。
	Answers map[string]answerRaw `json:"answers,omitempty"` // 保存各问题的答案。
}

// Answer 保存一个问题的类型化决策结果。
type Answer struct {
	Choice        string             // 保存选中的选项名。
	Probabilities map[string]float64 // 保存各选项的校准概率。
	Confidence    float64            // 保存置信度 0-1。
	Model         string             // 保存应答的模型版本。
}

// Client 是 systemone API 的最小 HTTP 客户端；零值不可用，用 New 创建。
type Client struct {
	endpoint string       // 保存端点 URL；默认 DefaultEndpoint。
	model    string       // 保存模型 ID；默认 DefaultModel。
	apiKey   string       // 保存 Bearer 鉴权的 API Key；不打印、不写日志。
	http     *http.Client // 保存 HTTP 客户端；测试可注入 httptest 服务器或超时。
}

// New 创建客户端；apiKey 必填，endpoint/model 为空时使用默认值。
func New(apiKey, endpoint, model string) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("jev: api key is required")
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpoint
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	return &Client{
		endpoint: endpoint,
		model:    model,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// SystemOne 一次提交 state 与全部问题，返回每个问题的类型化决策。
// state 是任意键值文本（本项目放证据单元）；questions 的键即答案的键。
// 任何 HTTP 错误、非 2xx 或响应解析失败都以 error 返回，由调用方决定回退。
func (c *Client) SystemOne(ctx context.Context, state map[string]string, questions map[string]Question) (map[string]Answer, error) {
	if c == nil {
		return nil, fmt.Errorf("jev: client is nil")
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("jev: at least one question is required")
	}
	payload := struct {
		Model     string              `json:"model"`
		State     map[string]string   `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{Model: c.model, State: state, Questions: questions}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.apiKey)

	started := time.Now()
	httpResponse, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("jev: call systemone: %w", err)
	}
	defer httpResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode > 299 {
		return nil, fmt.Errorf("jev: systemone returned %d after %s: %s",
			httpResponse.StatusCode, time.Since(started).Round(time.Millisecond), truncateBody(body, 300))
	}
	var parsed response
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}
	answers := make(map[string]Answer, len(parsed.Answers))
	for id, raw := range parsed.Answers {
		answers[id] = Answer{Choice: raw.Choice, Probabilities: raw.Probabilities, Confidence: raw.Confidence, Model: parsed.Model}
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("jev: response contains no answers")
	}
	return answers, nil
}

// truncateBody 把响应体压成单行并限长，供错误信息使用；不包含密钥。
func truncateBody(body []byte, limit int) string {
	single := strings.Join(strings.Fields(string(body)), " ")
	runes := []rune(single)
	if len(runes) <= limit {
		return single
	}
	return string(runes[:limit-1]) + "…"
}
