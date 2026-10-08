package eval

// 本文件实现失败聚类：把失败与错误 Trial 按归一化错误签名分组，不猜测根因。

import (
	"fmt"     // 拼接 Trial 引用。
	"sort"    // 按数量降序排列簇。
	"strings" // 前缀匹配与首行截断。
)

// FailureCluster 保存一类失败签名的统计。
type FailureCluster struct {
	Name        string   `json:"name"`         // 保存归一化后的失败签名。
	Count       int      `json:"count"`        // 保存属于该签名的 Trial 数量。
	TrialRefs   []string `json:"trial_refs"`   // 保存 "taskID#trial" 形式的 Trial 引用。
	SampleError string   `json:"sample_error"` // 保存一条原始错误样本，最长 160 字符。
}

// failureSignatures 保存有序的签名规则：前缀在前更 specific，兜底在最后。
var failureSignatures = []struct {
	prefix string
	name   string
}{
	{"policy denied", "policy-denied"},
	{"user denied", "user-denied"},
	{"verify command failed", "verify-failed"},
	{"verify command timed out", "verify-timeout"},
	{"does not match expected", "baseline-hash-mismatch"},
	{"path escapes workspace", "path-escape"},
	{"excluded from model tool access", "path-excluded"},
	{"repeated the same tool call", "doom-loop"},
	{"token budget exhausted", "budget-exhausted"},
	{"maximum agent steps reached", "max-steps"},
	{"was not called", "tool-missing"},
	{"arguments different from", "tool-arguments-mismatch"},
	{"does not contain expected substring", "output-mismatch"},
	{"timed out", "timeout"},
	{"context deadline exceeded", "timeout"},
	{"connection", "provider-network"},
	{"status code", "provider-http"},
	{"unauthorized", "provider-auth"},
}

// ClusterFailures 把全部失败与错误的 Trial 按签名聚类，按数量降序返回。
func ClusterFailures(results []TrialResult) []FailureCluster {
	clusters := make(map[string]*FailureCluster)
	for _, result := range results {
		if result.Outcome != TrialFailed && result.Outcome != TrialError {
			continue
		}
		name := classifyFailure(result.Error)
		cluster, exists := clusters[name]
		if !exists {
			cluster = &FailureCluster{Name: name}
			clusters[name] = cluster
		}
		cluster.Count++
		cluster.TrialRefs = append(cluster.TrialRefs, fmt.Sprintf("%s#%d", result.TaskID, result.Trial))
		if cluster.SampleError == "" {
			cluster.SampleError = compactClusterText(result.Error)
		}
	}
	ordered := make([]FailureCluster, 0, len(clusters))
	for _, cluster := range clusters {
		ordered = append(ordered, *cluster)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Count != ordered[j].Count {
			return ordered[i].Count > ordered[j].Count
		}
		return ordered[i].Name < ordered[j].Name
	})
	return ordered
}

// classifyFailure 按有序前缀规则归一化错误签名；无法归类时取首行做兜底签名。
func classifyFailure(message string) string {
	lowered := strings.ToLower(message)
	for _, signature := range failureSignatures {
		if strings.HasPrefix(lowered, signature.prefix) ||
			strings.Contains(lowered, signature.prefix) {
			return signature.name
		}
	}
	firstLine := message
	if index := strings.IndexByte(firstLine, '\n'); index >= 0 {
		firstLine = firstLine[:index]
	}
	firstLine = strings.TrimSpace(firstLine)
	if len(firstLine) > 60 {
		firstLine = firstLine[:60]
	}
	if firstLine == "" {
		return "unknown"
	}
	return "other: " + firstLine
}

// compactClusterText 把样本错误压成单行并限制 160 字符。
func compactClusterText(value string) string {
	single := strings.Join(strings.Fields(value), " ")
	runes := []rune(single)
	if len(runes) <= 160 {
		return single
	}
	return string(runes[:159]) + "…"
}
