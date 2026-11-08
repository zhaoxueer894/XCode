# XCode

XCode 是一个用 Go 写的多智能体编码工具。你给它一个任务（比如"修复这个 bug 并跑通测试"），它会在指定的工作区里读代码、改代码、运行命令，最后告诉你做了什么、结果如何。

## 多智能体设计

架构是 Orchestrator-Worker（编排者-工作者）。主智能体负责跟你对齐目标、拆解和派活，具体执行交给角色化的子智能体：

```text
编排者（主智能体）
  ├─ delegate → planner    拆解任务，不改文件
  ├─ delegate → developer  改代码、跑测试
  └─ delegate → reviewer   独立检查，给 pass / issues
```

**角色分工。** 三个内置角色各有自己的指令：planner 只拆解不动手；developer 负责改代码和跑验证；reviewer 只看代码和工作区的最终状态。角色可以用 `.xcode/skills/<角色名>.md` 自定义。

**上下文隔离。** 每次 delegate 都会创建一个全新的子智能体：独立的对话历史、独立的轮数预算，不继承父级上下文，做完只回传一份结论摘要。这是刻意设计——reviewer 看不到 developer 的推理过程，才能做出真正独立的判断。

**通信与轨迹。** 下行是任务包（角色 + 任务描述），上行是结论摘要，不共享聊天记录。所有子智能体的行为事件带 `agent_id` 汇入同一条 JSONL 轨迹，谁做了什么可回查。

**完成门禁。** 传入完成契约后，XCode 会在模型声称完成时运行验证命令；契约启用 `review` 时，再派一个全新上下文的 reviewer 复查，输出 `GATE-VERDICT: PASS` 或 `ISSUES`。被打回就继续修，直到通过或预算耗尽。编译、测试这些环境真实结果才是最终事实源。

想验证多智能体是否真的有收益？`xcode eval --no-delegate` 一键关闭委派，同一批任务对照单智能体跑分。

工具执行有安全约束：写文件前自动快照（可恢复），命令默认限定在工作区内，敏感操作需要你确认。

## 快速开始

需要 Go 1.26+。你可以通过本项目的“假模型”先建立手感，熟悉后再配置API KEY。它不是语言模型，而是一段写死在源码里的确定性逻辑，让你零成本把整条流程跑起来，以及给自动化测试和离线评测提供复现能力。

```bash
go build -o bin/xcode.exe ./cmd/xcode
bin/xcode.exe --provider demo "please read go.mod"
```

具体行为也就一条：把提示词的最后一个词当文件名，发起一次 `read_file`，然后照本宣科地返回结果。它不会理解你的任务，更不会真的修 bug——**真实任务请接下面的真实模型**。

## 配置模型

也支持 DeepSeek。可以直接使用仓库外的配置文本，无需把密钥复制进项目：

```bash
bin/xcode.exe --model-config C:/private/deepseek.txt "修复这个项目的问题并运行测试"
```

文本中的 `model:` 指定实际模型，`api-key:` 后填写密钥。默认启用思考并保留连续工具调用需要的思考状态；运行记录不包含密钥。也可使用 `--provider deepseek`，通过 `DEEPSEEK_API_KEY`、`DEEPSEEK_BASE_URL`、`DEEPSEEK_MODEL` 配置。

主模型用的是**阿里云百炼的通义千问**（默认 `qwen-plus`，可换成百炼上的其他模型），通过百炼的 OpenAI 兼容接口调用。按以下三步完成配置：

1. 在项目根目录创建 `.env` 文件（模板见 `.env.example`），填入你在百炼控制台拿到的 API Key：

   ```
   DASHSCOPE_API_KEY=sk-xxxxxxxx
   DASHSCOPE_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
   DASHSCOPE_MODEL=qwen-plus
   ```

2. Linux/macOS 下把权限收紧到 `chmod 600 .env`；这个文件已被 `.gitignore` 排除，不会被提交。

3. 运行时通过 `--provider bailian` 显式选择真实模型：

   ```bash
   bin/xcode.exe --provider bailian "修复 utils.go 里的越界问题并运行 go test"
   ```

   不想每次敲 flag 的话，在 `.xcode/config` 里写一行 `provider=bailian` 即可。注意：只配 `.env` 不切 provider 的话，跑的仍然是 demo 模型。

## 可选：压缩决策分类器（jev）

这是一个用于compact的进阶功能，不配置也不影响正常使用。

长对话会被压缩以节省上下文。压缩时 XCode 把历史拆成一个个"证据单元"，决定哪些保留、哪些截断、哪些丢弃——默认由内置规则决定。如果你想用模型来做这些决定，有两种后端：

- **local**：用百炼的 qwen 聊天模型当分类器，直接复用 `.env` 里的 Key，零额外配置。
- **jev**：调用 TypeSafe 的 systemone 分类服务。需要单独申请并设置三个环境变量：

  ```
  TYPESAFE_API_KEY=你的密钥
  TYPESAFE_ENDPOINT=服务端点地址
  TYPESAFE_MODEL=模型名
  ```

  然后在 `.xcode/config` 里写 `typed-classifier=jev`（不写默认 local），运行时加 `--typed-shadow` 开启。

shadow 指的是分类结果只记录、不生效——真实压缩仍走规则，方便对比模型和规则谁判得好。

## 怎么用

### 一次性任务

位置参数就是任务描述，整句用引号包起来：

```bash
bin/xcode.exe --provider bailian --workspace D:/path/to/repo "修复登录超时的 bug，改完跑通 go test"
```

运行中会实时显示每一步：读了哪些文件、执行了什么命令、策略判定和审批；结束后打印停止原因和本次 token 用量。

### 交互模式（多轮对话）

不带任务直接启动就进入 REPL，可以连续对话，上下文跨轮保留：

```bash
bin/xcode.exe --provider bailian
```

REPL 内置几个斜杠命令：

```text
/status             当前 provider、轮数、累计 token、已改文件、测试状态
/compact            压缩对话历史，释放上下文（工作笔记保留）
/trust go test      信任某个命令前缀，本会话内执行它不再弹审批
/clear              清空会话历史（项目记忆保留）
/exit               退出
```

退出时自动保存会话，下次启动会问你要不要接着上次继续。

### 中断恢复

任务跑到一半进程被杀（崩溃、断电、误关终端），加 `--resume` 从最后一个安全边界续跑，已完成的步骤不会重做：

```bash
bin/xcode.exe --provider bailian --resume
```

未完成的工具调用按副作用分别处理：只读的自动重试；改文件的先核对实际状态再重放；执行结果说不清的（比如一条 exec）一律列出来等你确认，绝不自作主张重跑。

### 完成门禁

默认情况下模型自己说完成就结束。如果任务有硬验收标准，写一份契约文件交给门禁把关：

```json
{
  "goal": "修复 utils.go 中的越界访问",
  "acceptance": ["go test 全部通过"],
  "review": true,
  "verify_commands": [
    { "name": "go test", "command": ["go", "test", "./..."] }
  ]
}
```

```bash
bin/xcode.exe --provider bailian --contract task.json "修复 utils.go 里的越界问题"
```

模型声称完成时，XCode 会在当前工作区真实运行 `verify_commands`，并派 reviewer 复查；不通过就打回继续修，直到通过或预算耗尽。

### 评测

新的外部评测入口和分阶段实施说明见 [evals/README.md](evals/README.md)。它运行真实 CLI，保存失败结果，统一计算主/子 agent 和摘要的模型预算，并提供 Harbor 适配和完整 patch 导出。

下面的 `xcode eval` 是内部小任务回归，不能据此宣称真实仓库能力或企业级可靠性。demo 模型只用于检查链路。

用固定任务集跑分，多 Trial 取均值和方差：

```bash
# 离线跑分（demo 假模型，免费、结果可复现）
bin/xcode.exe eval --trials 3 --label demo-baseline

# 真实模型跑分
bin/xcode.exe eval --provider bailian --trials 3 --label qwen-full

# 单/多智能体对照：--no-delegate 关闭委派，再跑一轮对比
bin/xcode.exe eval --provider bailian --trials 3 --label qwen-nodelegate --no-delegate

# 固化基线，之后每次改动后对比（有回归时退出码非零，可当合入门禁）
bin/xcode.exe eval baseline save qwen-full --run .eval/<run目录>
bin/xcode.exe eval compare --baseline qwen-full --current .eval/<新run目录>
```

每次评测输出全部 Trial 的 JSONL 轨迹和 `report.json` / `report.md` / `report.html` 三种报告（成功率、轮数、token、重复读取、方差、失败聚类）。

### 辅助命令

```text
xcode checkpoints                查看历次写前快照
xcode restore --id <快照ID>      恢复到指定快照（需交互确认）
xcode journal                    查看工具调用状态机（含崩溃窗口留下的 unknown）
xcode policy check --tool exec --arguments '{"command":["go","test"]}'
                                 预演一次策略判定，打印完整判定链路
xcode eval-fault                 故障注入评测：对比不同压缩/恢复策略在崩溃场景下的表现
```

## 常用配置

运行参数优先级：命令行 flag > 项目级 `.xcode/config` > 全局 `~/.xcode/config` > 内置默认值。常用的几项：

```text
provider=bailian          # demo / bailian / deepseek
mode=workspace-write      # plan / workspace-write / full-access
context-strategy=bounded  # bounded / none / sliding / handoff / compaction / decision-gated
context-budget=6000      # 模型可见历史的估算 token 目标；不含工具说明
max-steps=8               # 单次任务最大模型轮数
sandbox=host              # host / docker / docker-wsl
timeout=2m
```

其他可选文件：`.xcode/policy.json`（工具放行/拒绝规则）、`.xcode/skills/<角色>.md`（自定义 delegate 角色）、`.xcode/memory.md`（项目级记忆，每次运行注入）。

默认 `bounded` 保留任务、项目要求和最近的完整工具往返，将较早结果收敛为带 artifact 引用的证据记录。完整历史仍保存，当前任务或最新一次工具往返过大时可以超过目标，工具调用和结果不会被拆开。`none` 可显式关闭压缩。每次模型调用还会收到最新的共享请求/token 余额及剩余时间；这些提示不重复写入会话历史。观察到生产修改且其后的命令成功退出时，提示会要求立即收尾；修改后连续多次调用没有任何成功验证时，提示会要求先做一次聚焦验证再结束。预算只够最后一次请求时（剩余请求为 1 或剩余 token 不足 5%），该请求直接禁用工具，让模型产出最终回答，而不是发起一个结果永远无人消费的工具调用。

命令执行前的快照支持符号链接：保存、恢复链接本身，不读取链接目标。构建工具所需的 Go 缓存、并发设置及已激活的 Python 环境会传给命令，模型 API 密钥不传给命令子进程。

## 测试

```bash
go test ./...
go test -race ./...
```
