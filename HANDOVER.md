# XCode 交接文档(2026-11-11 更新)

写给接下来接手的同学。读完这一份就能知道项目做到哪了、下一步做什么、红线是什么。

## 一、项目是什么

XCode 是 COMP5584 课程小组项目:用 Go 写的多智能体编码 CLI。给它一个任务(如"修复这个 bug 并跑通测试"),它在指定工作区里读代码、改代码、跑命令,最后报告结果。架构是 Orchestrator-Worker:编排者派活给 planner / developer / reviewer 三个角色化子智能体,完成门禁用真实运行验证命令的结果把关,跨模型审查(reviewer 用不同供应商的模型)做盲区互补。

**截止日期 2026-11-23,还剩 13 天。** 交付物 = 代码 + 不超过 10 分钟视频 + slides + 贡献声明。评分里 MAS Design 占 30% 是最高权重项,所以提交的系统必须是完整多智能体流水线——消融实验数据就是 slides 的核心论据。

## 二、当前状态

### 代码(main 分支,已推送,最新提交 0b72706,工作区干净)

核心功能全部已实现并有测试覆盖:

- **编排模式** `orchestrate`:`single`(单智能体)/ `single-review`(单智能体 + 强制门禁审查)/ `pipeline`(强制 planner → developer → 跨模型 reviewer 流水线,编排者只许派活不许亲自改代码);留空 = 全工具智能体。
- **完成门禁 + 跨模型审查**:reviewer 用不同供应商模型(如 GLM);未配置或运行期失败时自动降级为同模型并三重警告(stderr 横幅 + `review.degraded` 事件 + 运行记录标记),绝不静默。
- **run 级进度账本**:主智能体与全部子智能体共用一本"是否有人改过生产代码"的账,探索门禁和运行时提示据此判定,晚期出生的子智能体不会被空白账本误伤。
- 其余:中断恢复(`--resume`)、上下文压缩(bounded 等策略)、快照回滚、eval 回归、故障注入评测。详见 [README.md](README.md)。

### 此后的增量(接手前必读)

- **模型集配置**:`.xcode/config` 改为槽位制——顶部 `developer=` / `reviewer=` 两个选择器,下方 `model.<名字>.key/base-url/id` 可扩充任意多个模型;jev 栏目完整(`jev=on|off` + `typesafe-*` 三键,off 时分类用开发者模型)。旧键兼容。
- **工作区指纹修复(消融 B/C 格的生死线)**:指纹现在遵循 .gitignore、大文件按"总大小+前 8MiB 哈希"、符号链接按链接文本、其他特殊文件按元数据——修复前指纹在真实仓库(node_modules/大文件)会 11 分钟扫不完或直接报错,门禁全灭。带回归测试。
- `jev` 开关与注释英文化已提交;single-review 已端到端真实验证(GLM reviewer 自主调工具核查、门禁 19 秒放行)。
- 运行教训:**改完代码必须 `go build -o bin/xcode.exe ./cmd/xcode` 重建再冒烟**(三次踩旧二进制的坑);真实模型在仓库工作区里跑 `go build` 会在根目录产出 `xcode.exe`(已 gitignore,别再 `git add -A` 扫进去);后台跑 harbor 用 setsid 并保持启动会话存活数秒。

### 评测进展(4 轮已完成)

- 四轮报告都在 `evals/` 下:`PILOT_REPORT.md`、`UNSEEN_TEN_REPORT.md`、`UNSEEN_R2_REPORT.md`、`UNSEEN_R3_REPORT.md`。评测框架说明见 [evals/README.md](evals/README.md)。
- 最新一轮(round 3,2026-11-10):10 道冻结未见题,多智能体首次开放委派。结果 8 道公平判定、3 道通过。**核心发现:10 道里 7 道 delegate 调用为零——编排者几乎总是自己做完,"开放委派"不等于"使用委派"**。所以 round 3 不构成多智能体 vs 单智能体的对照,这正是消融实验要解决的问题:用强制拓扑(pipeline)保证多智能体真实发生,再对比。
- 过程中根治了两个基础设施 bug:capture 脚本 Python 3.6 兼容(c5039b5)、非法流式工具调用不再杀死整个请求(57ccbfa),均有回归测试。

### 轻量评测线(2026-11-11 新增,与上面这条线并行)

`evals/lite/` 是独立第二条评测线,产物在 `.eval/lite/`,**不与 Harbor/SWE 线互相干扰**(不碰 `drive_ablation.sh`、`prepare_ablation.py`、`harbor_agent.py`,不占 `.eval/ablation/`)。

- **为什么**:SWE 线单题要 Docker + 镜像 + 几分钟到十几分钟,10 道题的样本量方差太大。轻量线用"小文件 + 单元测试",单题毫秒到数秒,可以扩到 100–200 道,消融(尤其 A/B/C 拓扑对照)才有统计意义。
- **三类题**:`algo`(单文件算法)、`module`(按 spec 实现 parser / 缓存 / 令牌桶等)、`bugfix`(50–200 行代码 + 失败测试)。Python 与 Go 两种语言,均无外部依赖。
- **当前状态**:25 道种子题已量产,`calibrate.py` 全部 25/25 校准通过(参考解通过 + 空补丁失败,可见测试与隐藏测试都得如此);已用离线 demo 模型和参考解脚本验证过完整链路(policy 放行 → 门禁拦停 → 导出 patch → 干净树跑隐藏测试判分)。
- **判分隔离**:agent 只看到 `starter/`;判分在另一份干净副本上应用候选 patch 后再叠 `hidden/`,因此**改坏或删掉可见测试都不能改变成绩**(已实测:篡改/删除可见测试仍判 fail,空补丁也判 fail)。可见测试命令进完成门禁,隐藏测试只在 runner 侧跑。
- **待办**:尚未跑真实模型轮(会把额度计入正在跑的 B/C 消融),等消融数据出来后用 `--cells single,single-review,pipeline` 跑第一批真实对照。命令与红线见 [evals/lite/README.md](evals/lite/README.md)。


## 三、接下来的任务(按顺序)

### 1. 消融实验(最优先,数据是 slides 的命根子)

A/B/C 三格,同一批 round 3 冻结题(10 道)、同预算(每题 20 万 token)、同一个**开跑前从当前 main 重建**的 `bin/xcode-linux-amd64`(driver 会把当次 sha 快照进每格;切勿用指纹修复之前的旧二进制,B/C 格会被指纹 bug 卡死),只有协作拓扑不同:

| 格 | 模式 | 审查 |
|---|---|---|
| A | single | 无门禁审查 |
| B | single-review | GLM 跨模型审查 |
| C | pipeline(强制 planner→developer→reviewer) | GLM 跨模型审查 |

**前置条件(缺一不可):**

- Driver 在开跑时从 `.xcode/config` 读取当前开发和审查模型的地址、模型 ID 和密钥，各做一次最小调用。任一调用失败就终止，避免 B/C 在审查链路不可用时启动。
- 用户明确批准后才跑(driver 文件头就是这么写的)。

**怎么跑(两步,顺序不能反):**

```bash
# 第 1 步,Windows 侧(Git Bash,仓库根目录):重建 Linux 二进制
#    WSL 里没有 Go,编不了;driver 会校验二进制比 HEAD 新,旧了直接拒跑
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/xcode-linux-amd64 ./cmd/xcode

# 第 2 步,WSL 里(仓库根目录),detached 启动(直接后台跑会被 WSL 会话回收杀掉):
setsid sh evals/drive_ablation.sh >> .eval/ablation-driver.log 2>&1 < /dev/null &
sleep 30 && tail -5 .eval/ablation-driver.log   # 确认 preflight 过了再离开
```

Driver 自动执行模型预检、二进制新鲜度校验、三格准备和逐格 Harbor 运行（每格 2 路并发）。**全程约 6-7 小时**（每格约 2 小时）。每 10–15 分钟查看一次日志。启动和续跑都用 `>>` 追加 driver 日志。

每格跑完的验收:`.eval/ablation/<cell>/harbor/xcode-v1/` 下应有 **10 个** trial 目录(各含 result.json、agent/events.jsonl、model.patch、verifier 判分),console.log 末尾有该格 exit 码。三格全跑完才算消融结束,进入分析(见下一节)。

Driver 使用 `/mnt/d/Projects/XCode` 作为仓库路径。三个格子都向 Harbor 传入同一份 agent 出网域名列表，取自当前模型端点。

**中断与接管(任何 agent/机器都适用):**

- **只有跑评测的 agent 断了**(token 耗尽、会话掉线):驱动是 setsid 脱离会话跑的,**评测不会停**。新 agent 接管后只需继续看 `.eval/ablation-driver.log` 和各格 console.log,什么都不用重启。
- **驱动/机器也断了**(WSL 重启、断电、脚本失败):重新执行上面两步(Windows 重建二进制 → WSL setsid 重启 driver)即可。**driver 支持断点续跑**:已完成的格子自动跳过;跑了一半的格子只把"没有 result.json 的题"放到新 job(xcode-v2、v3…)里续跑,已完成的 trial 原样保留、绝不重跑。分析时按格子合并全部 xcode-v* 目录。
- **续跑前唯一要人工确认的**:先 `tail` 各格 console.log 看中断原因——若是 GLM 余额耗尽这类会复现的问题,先解决再续跑,否则续跑的格子会全程降级。

**铁律：不重跑已完成的题、不删除记录、失败原样留盘。** 发现可能影响数据有效性的问题时报告用户，由用户决定如何归类。`prepare_ablation.py` 只校验已有格子目录。准备状态不完整或清单与当前配置不符时，停下来报告。

### 2. 消融结果分析 + 报告

仿照 `evals/UNSEEN_R3_REPORT.md` 的格式写:每格通过数、token 成本对比(文献里多智能体约 4 倍 token 换独立审查 +10 分的收益,我们自己的数据是核心)、失败聚类。证据目录 `.eval/ablation/<cell>/harbor/`。

### 3. 课程交付物(deadline 2026-11-23)

- **≤10 分钟视频**:演示完整流水线跑一个真实任务,编排者派活 → developer 改码 → 门禁验证 → 跨模型 reviewer 放行/打回。消融数据出來后录,论证"我们验证了什么、结论是什么"。
- **Slides**:MAS Design 30% 最高权重,重点讲多智能体设计决策(上下文隔离、强制拓扑、独立审查、降级不静默)+ 消融对照数据。
- **贡献声明**:按组员分工写。

### 4. 收尾杂项

- ~~提交工作区 8 个文件~~ 已完成。
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
# 流水线模式 + 跨模型审查(配置:reviewer=glm 指向 model.glm.* 槽位)
bin/xcode.exe --orchestrate pipeline "任务描述"
```

| 东西 | 位置 |
|---|---|
| 模型密钥配置 | `.xcode/config`(gitignore,不入库;槽位制,模板 `.xcode/config.example`) |
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
