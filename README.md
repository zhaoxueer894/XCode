# XCode

基于大语言模型的多智能体编码系统，Go 实现。XCode 在真实工作区中完成编码任务：理解需求、检索代码、修改文件、运行验证并报告证据。系统采用 Orchestrator-Worker 多智能体架构，内置工具安全管线与轨迹评测闭环。

## 多智能体协作机制

- **角色化子智能体**：编排者（主智能体）通过 `delegate` 工具将子任务委派给角色化子智能体，内置三个角色——`planner`（分解任务，不修改文件）、`developer`（最小化修改并运行验证）、`reviewer`（独立审查，输出 pass / issues-found）；角色可通过 `.xcode/skills/<role>.md` 自定义。
- **独立上下文**：每次委派创建全新的子智能体实例（独立消息历史、独立执行循环、独立停止条件），不继承父级对话，仅通过结构化任务包下行、摘要与证据上行。
- **事件桥接**：子智能体事件携带 `agent_id` 与 `agent.spawned` / `agent.completed` 生命周期标记，汇入统一事件流与轨迹。
- **完成门禁**：智能体声称任务完成时，Harness 强制发起 fresh-context 的 reviewer 独立审查（`GATE-VERDICT: PASS / ISSUES`），不通过则将失败证据回注循环继续修复；环境真实结果（编译、测试退出码）是最终事实源，不采信模型自述。
- **对照评测**：`xcode eval --no-delegate` 可关闭委派做单智能体 A/B 对照，量化多智能体协作的收益与成本。

## 工具与安全

- 内置工具：`list_files`、`search_text`、`read_file`、`apply_patch`、`exec`、`git_diff`、`read_skill`。
- 统一执行管线：参数规范化 → 策略决策（`allow / ask / deny`）→ 审批（once / session）→ 写前快照 → 沙箱执行（`host` / `docker` / `docker-wsl`）→ 输出虚拟化 → 状态落库。
- ToolCall 状态机：`planned → approved → started → succeeded / failed / unknown`；进程崩溃窗口判 `unknown`，非幂等操作绝不自动重放。
- 能力档位：`plan` / `workspace-write` / `full-access`，默认 `workspace-write`；命令超时终止完整进程树，输出上限 2 MiB。

## 上下文与记忆

- Token 预算与四种上下文策略：`none` / `sliding` / `handoff` / `compaction`。
- 工具结果虚拟化：完整结果存入 Artifact Store，上下文只保留预览，可按 `artifact_id` 回取。
- ReadSet 去重：内容哈希 + Context Epoch 判定重复读取，内容变更自动失效。
- 分层记忆：L0 轮内工作笔记、L1 会话笔记、L2 项目记忆（`.xcode/memory.md`）。

## 评测闭环

- 每轮运行产生 append-only JSONL 轨迹（事件、策略、审批、快照、工具状态全量记录）。
- 多 Trial 评测：成功率、轮次、工具调用、token、耗时与方差报告（JSON / Markdown / HTML）。
- 基线锁定与对比：`xcode eval baseline save` / `xcode eval compare`，五级判定（improved / regressed / unchanged / new / missing），存在回归时退出码非零。
- 失败聚类与故障评测：失败签名归簇、故障注入任务与 Trace Replay。

## 快速开始

```bash
go build -o bin/xcode.exe ./cmd/xcode

# 离线演示（无需 API Key，内置确定性模型）
bin/xcode.exe --provider demo "读取 README.md 并总结"

# 真实模型：复制 .env.example 为 .env，填入密钥（该文件已被 Git 忽略）
bin/xcode.exe "修复 xxx.go 中的越界 bug 并运行测试"
```

## 配置

| 文件 | 用途 |
|---|---|
| `.xcode/config` | 运行配置：provider、mode、context-strategy、max-steps、sandbox、timeout 等；优先级 CLI flag > 项目级 > `~/.xcode/config` > 内置默认 |
| `.xcode/policy.json` | 工具策略规则（路径前缀、命令前缀的 allow/deny） |
| `.xcode/skills/<role>.md` | delegate 角色定义 |
| `.xcode/memory.md` | L2 项目记忆，运行时只读注入 |
| `.env` | 模型密钥（参考 `.env.example`；保持 0600 权限，禁止提交） |

## 目录结构

```
cmd/xcode/          CLI 入口（run / eval / checkpoint / journal / policy / resume 等子命令）
internal/
  harness/          Agent 循环、delegate 多智能体、完成门禁、会话与运行日志
  executor/         统一工具执行器（信号量、审批、journal）
  policy/           策略引擎与规则文件
  sandbox/          沙箱后端（host / docker / docker-wsl）
  checkpoint/       写前快照与恢复
  recovery/         崩溃恢复与副作用对账
  contextmgr/       上下文投影、压缩策略、决策门控压缩
  memory/           L0-L2 分层记忆与技能加载
  tools/            全部工具实现
  workspace/        工作区索引、忽略规则、ReadSet、指纹
  artifactstore/    内容寻址 Artifact 存储
  eventstore/       JSONL 事件存储
  eval/             评测运行器、报告、基线对比、失败聚类
  faulteval/        长任务故障注入评测与 Trace Replay
  model/            模型适配（demo、bailian 流式协议、jev 分类器）
  config/           配置文件加载
testdata/           评测任务目录
```

## 测试

```bash
go test ./...
go test -race ./...
```
