# XCode

XCode 是一个用 Go 写的多智能体编码工具。你给它一个任务（比如"修复这个 bug 并跑通测试"），它会在指定的工作区里读代码、改代码、运行命令，最后告诉你做了什么、结果如何。整个过程中所有文件改动、命令执行和审查结论都有记录，可以事后查看。

它不是把同一个模型调几次，而是由几个角色不同的智能体分工协作：

- **编排者**接你的任务，决定要不要拆解、派活；
- **developer** 负责动手改代码、跑验证；
- **reviewer** 是一个独立检查者，它看不到 developer 的思考过程，只看最终的代码和工作区状态，给出通过或打回。模型声称"做完了"时，XCode 会强制让 reviewer 检查一遍，环境里的真实结果（编译、测试是否通过）说了算。

工具执行有安全约束：写文件之前会做快照（可恢复），命令默认限定在工作区内，敏感操作需要你确认。

## 快速开始

需要 Go 1.26+。不需要任何 API Key，内置一个确定性演示模型：

```bash
go build -o bin/xcode.exe ./cmd/xcode
bin/xcode.exe --provider demo "please read go.mod"
```

demo 模型只会解析提示词里最后一个词当作文件名去读，仅用于跑通流程，别对它抱期望。真实任务请接下面的真实模型。

## 配置模型

主模型用的是**阿里云百炼的通义千问**（默认 `qwen-plus`，可换成百炼上的其他模型），通过百炼的 OpenAI 兼容接口调用。配置三步：

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

这是一个实验性功能，不配置完全不影响正常使用。

长对话会被压缩以节省上下文。压缩时 XCode 把历史拆成一个个"证据单元"，决定哪些保留、哪些截断、哪些丢弃——默认由内置规则决定。如果你想用模型来做这些决定，有两种后端：

- **local**：用百炼的 qwen 聊天模型当分类器，直接复用 `.env` 里的 Key，零额外配置。这是对照基线。
- **jev**：调用 TypeSafe 的 systemone 分类服务。需要单独申请并设置三个环境变量：

  ```
  TYPESAFE_API_KEY=你的密钥
  TYPESAFE_ENDPOINT=服务端点地址
  TYPESAFE_MODEL=模型名
  ```

  然后在 `.xcode/config` 里写 `typed-classifier=jev`（不写默认 local），运行时加 `--typed-shadow` 开启。

shadow 指的是分类结果只记录、不生效——真实压缩仍走规则，方便对比模型和规则谁判得好。

## 常用配置

运行参数优先级：命令行 flag > 项目级 `.xcode/config` > 全局 `~/.xcode/config` > 内置默认值。常用的几项：

```text
provider=bailian          # demo 或 bailian
mode=workspace-write      # plan / workspace-write / full-access
context-strategy=none     # none / sliding / handoff / compaction
max-steps=8               # 单次任务最大模型轮数
sandbox=host              # host / docker / docker-wsl
timeout=2m
```

其他可选文件：`.xcode/policy.json`（工具放行/拒绝规则）、`.xcode/skills/<角色>.md`（自定义 delegate 角色）、`.xcode/memory.md`（项目级记忆，每次运行注入）。

## 测试

```bash
go test ./...
go test -race ./...
```
