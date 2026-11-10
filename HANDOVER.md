# XCode 交接文档(2026-11-10)

写给接下来接手的同学。读完这一份就能知道项目做到哪了、下一步做什么、红线是什么。

## 一、项目是什么

XCode 是 COMP5584 课程小组项目:用 Go 写的多智能体编码 CLI。给它一个任务(如"修复这个 bug 并跑通测试"),它在指定工作区里读代码、改代码、跑命令,最后报告结果。架构是 Orchestrator-Worker:编排者派活给 planner / developer / reviewer 三个角色化子智能体,完成门禁用真实运行验证命令的结果把关,跨模型审查(reviewer 用不同供应商的模型)做盲区互补。

**截止日期 2026-11-23,还剩 13 天。** 交付物 = 代码 + 不超过 10 分钟视频 + slides + 贡献声明。评分里 MAS Design 占 30% 是最高权重项,所以提交的系统必须是完整多智能体流水线——消融实验数据就是 slides 的核心论据。

## 二、当前状态

### 代码(main 分支,已推送,最新提交 41d4d04)

核心功能全部已实现并有测试覆盖:

- **编排模式** `orchestrate`:`single`(单智能体)/ `single-review`(单智能体 + 强制门禁审查)/ `pipeline`(强制 planner → developer → 跨模型 reviewer 流水线,编排者只许派活不许亲自改代码);留空 = 全工具智能体。
- **完成门禁 + 跨模型审查**:reviewer 用不同供应商模型(如 GLM);未配置或运行期失败时自动降级为同模型并三重警告(stderr 横幅 + `review.degraded` 事件 + 运行记录标记),绝不静默。
- **run 级进度账本**:主智能体与全部子智能体共用一本"是否有人改过生产代码"的账,探索门禁和运行时提示据此判定,晚期出生的子智能体不会被空白账本误伤。
- 其余:中断恢复(`--resume`)、上下文压缩(bounded 等策略)、快照回滚、eval 回归、故障注入评测。详见 [README.md](README.md)。

### 工作区有 8 个文件未提交(2026-11-10 晚的状态)

内容分两类:

1. **注释翻译**:6 个文件的中文注释翻成了英文(config.example、provider_settings、run_record、run_options、runtime_progress、types 等),无功能变化。
2. **唯一的功能改动:`jev` 开关**:`internal/config/config.go` 新增 `jev` 配置键,`cmd/xcode/main.go` 里 `jev=on` 强制用 TypeSafe systemone 分类器、`jev=off` 回退本地聊天模型(原先只有 `typed-classifier` 键,现在 jev 键优先)。`.xcode/config.example` 重写为英文并补了 jev 配置段。

**已验证:`go build` / `go vet` / `go test ./...` 全部通过。** 接手后可以直接提交(建议先跑一遍 `go test -race ./...`);提交前记得交叉编译验证 linux/darwin(教训:曾因平台相关代码没在 linux 编过而返工)。

### 评测进展(4 轮已完成)

- 四轮报告都在 `evals/` 下:`PILOT_REPORT.md`、`UNSEEN_TEN_REPORT.md`、`UNSEEN_R2_REPORT.md`、`UNSEEN_R3_REPORT.md`。评测框架说明见 [evals/README.md](evals/README.md)。
- 最新一轮(round 3,2026-11-10):10 道冻结未见题,多智能体首次开放委派。结果 8 道公平判定、3 道通过。**核心发现:10 道里 7 道 delegate 调用为零——编排者几乎总是自己做完,"开放委派"不等于"使用委派"**。所以 round 3 不构成多智能体 vs 单智能体的对照,这正是消融实验要解决的问题:用强制拓扑(pipeline)保证多智能体真实发生,再对比。
- 过程中根治了两个基础设施 bug:capture 脚本 Python 3.6 兼容(c5039b5)、非法流式工具调用不再杀死整个请求(57ccbfa),均有回归测试。

## 三、接下来的任务(按顺序)

### 1. 消融实验(最优先,数据是 slides 的命根子)

A/B/C 三格,同一批 round 3 冻结题(10 道)、同预算(每题 20 万 token)、同二进制(`bin/xcode-linux-amd64`,sha256 前缀 `c0f80257`),只有协作拓扑不同:

| 格 | 模式 | 审查 |
|---|---|---|
| A | single | 无门禁审查 |
| B | single-review | GLM 跨模型审查 |
| C | pipeline(强制 planner→developer→reviewer) | GLM 跨模型审查 |

**前置条件(缺一不可):**

- **GLM 充值**:智谱 open.bigmodel.cn 账户余额不足(API 返回 1113 错误)。B/C 两格的跨模型审查依赖 GLM。driver 开头的 preflight 会做一次最小调用,余额不够直接终止——这是刻意设计,避免 B/C 全程降级污染数据。**充值前不要跑。**
- 用户明确批准后才跑(driver 文件头就是这么写的)。

**怎么跑:**

```bash
# WSL 里,从仓库根目录
sh evals/drive_ablation.sh
```

driver 会自动:preflight 验 GLM 余额 → `evals/prepare_ablation.py` 生成三格目录 → 逐格跑 harbor(每题 2 次试跑),每格落盘 manifest、console.log、git rev、二进制快照。注意 driver 里路径写死为 `/mnt/d/Projects/XCode`,代理写死为网关 IP 的 7897 端口,换机器要改。

**铁律:不重跑、不删除、失败留盘。** 失败的格子照样留在 `.eval/ablation/` 里,分析时归类为基础设施失败。绝对不允许挑着重跑"好看"的结果——这是课程项目,数据必须真实。另外 `prepare_ablation.py` 拒绝覆盖已存在的格子目录,所以只有在**还没跑过任何东西**的前提下才允许手动删掉重来。

### 2. 消融结果分析 + 报告

仿照 `evals/UNSEEN_R3_REPORT.md` 的格式写:每格通过数、token 成本对比(文献里多智能体约 4 倍 token 换独立审查 +10 分的收益,我们自己的数据是核心)、失败聚类。证据目录 `.eval/ablation/<cell>/harbor/`。

### 3. 课程交付物(deadline 2026-11-23)

- **≤10 分钟视频**:演示完整流水线跑一个真实任务,编排者派活 → developer 改码 → 门禁验证 → 跨模型 reviewer 放行/打回。消融数据出來后录,论证"我们验证了什么、结论是什么"。
- **Slides**:MAS Design 30% 最高权重,重点讲多智能体设计决策(上下文隔离、强制拓扑、独立审查、降级不静默)+ 消融对照数据。
- **贡献声明**:按组员分工写。

### 4. 收尾杂项

- 提交工作区那 8 个文件的改动(见上)。
- 可选研究方向(时间富余才做):让委派在"开放"模式下也真实发生——任务提示词引导。pipeline 模式已从机制上解决,消融 C 格就是在验证它。

## 四、速查表

```bash
# 编译(Windows)
go build -o bin/xcode.exe ./cmd/xcode
# 交叉编译 linux(消融二进制就是这么构建的)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/xcode-linux-amd64 ./cmd/xcode
# 测试
go test ./... && go test -race ./...

# 跑一个任务
bin/xcode.exe --workspace D:/path/to/repo "修复 bug 并跑通 go test"
# 流水线模式 + 跨模型审查(配置文件里 reviewer-provider=glm)
bin/xcode.exe --orchestrate pipeline "任务描述"
```

| 东西 | 位置 |
|---|---|
| 模型密钥配置 | `.xcode/config`(gitignore,不入库;模板 `.xcode/config.example`) |
| 评测证据 | `.eval/<轮次>/`,如 `.eval/unseen-r3/`、`.eval/ablation/`(消融跑完后) |
| 冻结题清单 | `evals/unseen-r3.json` 等 |
| 评测轮报告 | `evals/*_REPORT.md` |
| 评测框架说明 | `evals/README.md` |
| Harbor 虚拟环境 | `.cache/harbor-linux/`(WSL 里) |

## 五、红线(务必遵守)

1. **不伪造、不美化结果。** 失败就是失败,基础设施失败单独归类,这是全部评测工作的信用基础。
2. **所有 artifact 留档。** events.jsonl、patch、报告、二进制哈希都是 slides 和答辩的证据链。
3. **密钥不入库。** 只写 `.xcode/config`(已 gitignore)或全局 `~/.xcode/config`。
4. **消融跑之前必须:GLM 充值 + 用户批准。** 半吊子状态跑出来的降级数据全废。
5. 提交前:`go test ./...` + linux/darwin 交叉编译各过一遍。
