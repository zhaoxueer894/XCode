package main

// 本文件实现类型化 shadow 分类器的统一装配：
// 一次性命令、REPL 与故障评测三条路径共用同一套接线——凡是决策门控压缩发生的地方，
// local 或 jev 分类后端"能跑"；是否真的跑由用户通过 flag 与配置决定。
// 约束：实现层接入完成后，发起真实调用前必须得到用户允许（费用与外发内容由用户把关）。

import (
	"fmt"     // 构造错误。
	"strings" // 归一化后端名与选项。

	"xcode/internal/config"        // 供应商密钥与端点的统一取值链。
	"xcode/internal/contextmgr"    // 投影管理器与决策器。
	"xcode/internal/harness"       // Model 与协议类型。
	"xcode/internal/model/bailian" // local 分类器的独立聊天客户端。
	"xcode/internal/model/jev"     // TypeSafe systemone 客户端。
)

// resolveLocalClassifier 解析 local 分类后端使用的聊天模型：
// 配置了独立端点+模型时构建专用客户端（与主模型解耦），否则复用主模型；
// demo 主模型不具备真实分类能力且无独立配置时返回 nil（不采集 shadow）。
func resolveLocalClassifier(cfg config.Config, provider string, mainModel harness.Model, endpoint, model string) (harness.Model, error) {
	if strings.TrimSpace(endpoint) != "" && strings.TrimSpace(model) != "" {
		key := classifierKey(cfg)
		if key == "" {
			return nil, fmt.Errorf("classifier API key is empty (config bailian-api-key or env DASHSCOPE_API_KEY)")
		}
		return bailian.New(bailian.Config{
			APIKey:  key,
			BaseURL: endpoint,
			Model:   model,
		})
	}
	if strings.EqualFold(provider, "demo") {
		return nil, nil
	}
	return mainModel, nil
}

// mountTypedShadow 把类型化决策器挂到投影管理器上；backend 为 jev 或 local（默认）。
// effective=false 挂为 shadow（判定只记录不生效）；effective=true 挂为生效决策器（拍板，
// 失败/低置信度自动回退规则）。只负责"能跑"的接线；是否启用由调用方的 flag/配置决定。
func mountTypedShadow(manager *contextmgr.Manager, cfg config.Config, backend, provider string, mainModel harness.Model, endpoint, model string, effective bool) error {
	decider := func(classify contextmgr.ClassificationFunc) *contextmgr.TypedModelDecider {
		return contextmgr.NewTypedModelDecider(classify, 0.6)
	}
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "jev":
		key, jevEndpoint, jevModel := typesafeSettings(cfg)
		client, err := jev.New(key, jevEndpoint, jevModel)
		if err != nil {
			return err
		}
		if effective {
			manager.SetCompactionDecider(decider(buildJevSystemOneClassify(client)))
		} else {
			manager.SetShadowDecider(decider(buildJevSystemOneClassify(client)))
		}
		return nil
	default: // local：聊天 LLM 分类（Jev 诞生前的常用方案，作为对照基线）。
		classifier, err := resolveLocalClassifier(cfg, provider, mainModel, endpoint, model)
		if err != nil {
			return err
		}
		if classifier == nil {
			return nil
		}
		if effective {
			manager.SetCompactionDecider(decider(buildTypedClassify(classifier)))
		} else {
			manager.SetShadowDecider(decider(buildTypedClassify(classifier)))
		}
		return nil
	}
}
