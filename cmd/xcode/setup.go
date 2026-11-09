package main

// 本文件实现首次运行向导与 demo/Key 矛盾检测：
// provider 未配置时用菜单让用户选择并写入全局 ~/.xcode/config；
// 已配置某真实供应商 Key 但仍默认 demo 时用 Y/n 确认切换。
// 控制台读取与审批输入共用 readApprovalLine（CONIN$ / /dev/tty），
// 无控制台（管道、CI、无终端）时降级为提示并保持 demo，绝不阻塞无人值守运行。

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"xcode/internal/config"
)

// readAnswer 从标准输入读一行菜单答案；只在 stdinIsTerminal 之后调用。
func readAnswer() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

// wizardProviders 是向导菜单顺序：真实供应商在前，demo 兜底在后。
var wizardProviders = []string{"bailian", "deepseek", "demo"}

func wizardProviderLabel(provider string) string {
	switch provider {
	case "bailian":
		return "通义千问（阿里云百炼，推荐）"
	case "deepseek":
		return "DeepSeek"
	case "demo":
		return "demo（离线假模型，不联网，仅验证链路）"
	}
	return provider
}

// parseWizardChoice 解析菜单输入：接受序号或供应商名；ok=false 表示无法识别。
func parseWizardChoice(input string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(input))
	if trimmed == "" {
		return "", false
	}
	if index, err := strconv.Atoi(trimmed); err == nil {
		if index >= 1 && index <= len(wizardProviders) {
			return wizardProviders[index-1], true
		}
		return "", false
	}
	for _, provider := range wizardProviders {
		if trimmed == provider {
			return provider, true
		}
	}
	return "", false
}

// printProviderMenu 打印编号菜单与配置文件路径提示；首次向导与 REPL /model 共用。
func printProviderMenu(title, globalPath string) {
	fmt.Println(title)
	for index, provider := range wizardProviders {
		fmt.Printf("  %d) %s\n", index+1, wizardProviderLabel(provider))
	}
	fmt.Printf("使用其他模型或自定义端点：请编辑 %s\n", globalPath)
}

// runSetupWizard 在 provider 未出现在任何配置文件时介入：
//  1. 已有某真实供应商的 Key（配置文件或环境变量）→ Y/n 确认是否从 demo 切换；
//  2. 完全没有配置 → 编号菜单选择，写入全局 ~/.xcode/config。
//
// 只在交互式终端（stdin 为字符设备）触发；管道、重定向与 CI 直接降级提示。
// 返回生效 provider、配置是否被本次修改。选择真实供应商但 Key 缺失时打印
// 指引并返回错误——不静默降级 demo，避免"以为在用真模型"的误解。
func runSetupWizard(appConfig config.Config) (string, bool, error) {
	if appConfig.ProviderSet {
		return appConfig.Provider, false, nil
	}
	if !stdinIsTerminal() {
		noticeSetupSkipped(appConfig)
		return appConfig.Provider, false, nil
	}

	globalPath, err := config.GlobalPath()
	if err != nil {
		return appConfig.Provider, false, nil // 定位不到用户目录时静默跳过
	}

	// 矛盾检测：Key 已存在但 provider 仍是内置默认的 demo。
	if configured := detectConfiguredRealProvider(appConfig); configured != "" {
		fmt.Printf("检测到 %s 的 API Key，但当前默认使用 demo 假模型。切换到 %s？[Y/n] ", configured, configured)
		answer, readErr := readAnswer()
		if readErr != nil {
			return appConfig.Provider, false, nil
		}
		if isNoAnswer(answer) {
			return appConfig.Provider, false, nil
		}
		if err := config.Upsert(globalPath, "provider", configured); err != nil {
			return appConfig.Provider, false, err
		}
		fmt.Printf("✓ 已设置 provider=%s（写入 %s）\n", configured, globalPath)
		return configured, true, nil
	}

	// 首次运行菜单；菜单始终展示配置文件路径，用户随时知道去哪改。
	printProviderMenu("首次使用 XCode：选择要使用的模型", globalPath)
	choice := ""
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Print("输入序号并回车：")
		answer, readErr := readAnswer()
		if readErr != nil {
			fmt.Printf("[notice] 无法读取输入，保持 demo。配置真实模型：编辑 %s\n", globalPath)
			return appConfig.Provider, false, nil
		}
		if provider, ok := parseWizardChoice(answer); ok {
			choice = provider
			break
		}
		fmt.Println("无法识别，请输入 1-3。")
	}
	if choice == "" {
		return appConfig.Provider, false, nil
	}
	if err := config.Upsert(globalPath, "provider", choice); err != nil {
		return choice, false, err
	}
	fmt.Printf("✓ 已设置 provider=%s（写入 %s）\n", choice, globalPath)
	if choice == "demo" {
		return choice, true, nil
	}
	if !providerKeyConfigured(appConfig, choice) {
		fmt.Printf("请编辑 %s，在 %s 后填入你的 API Key，保存后重新运行 xcode。\n", globalPath, providerKeyHint(choice))
		return choice, true, fmt.Errorf("provider %s requires an API key before it can run", choice)
	}
	return choice, true, nil
}

func isNoAnswer(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "n", "no":
		return true
	}
	return false
}

// noticeSetupSkipped 在无法交互时给出明确的配置指引，本次运行保持 demo。
// 已检测到某真实供应商的 Key 时给出更精确的切换指引。
func noticeSetupSkipped(appConfig config.Config) {
	globalPath, err := config.GlobalPath()
	hint := "(无法定位全局配置路径)"
	if err == nil {
		hint = globalPath
	}
	if configured := detectConfiguredRealProvider(appConfig); configured != "" {
		fmt.Printf("[notice] 检测到 %s 的 API Key，但未设置 provider，本次仍使用 demo 假模型。\n"+
			"[notice] 在 %s 写入 provider=%s 即可切换。\n", configured, hint, configured)
		return
	}
	fmt.Printf("[notice] 未配置模型，本次使用 demo 假模型。配置真实模型：编辑 %s\n"+
		"[notice]（写入 provider=bailian 与 bailian-api-key=sk-... 等键），或在交互式终端中重新运行。\n", hint)
}
