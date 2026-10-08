// 负责管理可调用的工具
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// 映射
type Registry struct {
	tools map[string]Tool // key是工具名，value是Tool接口对象
}

// 创建工具注册表，传入任意数量工具，返回注册表
func NewRegistry(tools ...Tool) (*Registry, error) {
	// 预分配
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, tool := range tools {
		name := tool.Spec().Name
		if name == "" {
			return nil, fmt.Errorf("tool name is empty")
		}
		// 同名工具不允许
		if _, exists := r.tools[name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", name)
		}
		r.tools[name] = tool
	}

	return r, nil
}

// 获取所有工具列表
func (r *Registry) Specs() []ToolSpec {
	// 创建独立切片，避免调用者直接修改Registry内部map
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, tool := range r.tools {
		specs = append(specs, tool.Spec())
	}
	// Go的map遍历顺序不稳定，所以要按名称排序
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Name < specs[j].Name
	})
	return specs
}

// Tool 按名称返回已注册的工具，供统一 Executor 检查其执行前意图。
func (r *Registry) Tool(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// Execute: 根据工具名查找工具，再把参数交给工具执行并返回结构化结果
func (r *Registry) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	tool, ok := r.tools[call.Name]
	if !ok {
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	return tool.Execute(ctx, json.RawMessage(call.Arguments))
}
