# XCode 外部评测

这里评测实际交付的 XCode CLI，不另写一份 agent 循环。

目前完成的是第 0 阶段：批量运行入口、运行记录、共享预算、Harbor 适配、代码修改导出和基础验证。还没有形成公开基准分数，也没有宣称已经达到企业级。

## 本地批量入口

先编译：

```powershell
go build -o bin/xcode.exe ./cmd/xcode
```

凭据通过环境变量传入（`--model-config`/`--model-key-index` 已随单文件配置改造移除；密钥仍不进仓库）：

```powershell
$env:DEEPSEEK_API_KEY = "sk-..."   # 或从仓库外的私人文件读取后设置
bin/xcode.exe --isolated --non-interactive --provider deepseek --workspace D:/path/to/task --policy evals/policies/go-tests.json --no-delegate --provider-retries 0 --max-model-calls 100 --max-tokens 200000 --max-steps 100 --timeout 20m --event-log .eval/my-run/events.jsonl --artifact-dir .eval/my-run/artifacts --result-file .eval/my-run/result.json --prompt-file D:/path/to/instruction.txt
```

每次使用新的输出目录，已有结果文件不会被覆盖。命令执行仍经过策略；不在批准规则里的命令需要审批，而无人值守模式会直接拒绝审批，不会等待键盘输入。上述 Go 规则允许执行项目测试，测试本身会执行代码；只对可信任务或隔离环境使用。

`--provider deepseek` 读取 `DEEPSEEK_API_KEY`、`DEEPSEEK_BASE_URL`、`DEEPSEEK_MODEL`。默认模型 `deepseek-flash`；明确记录 `thinking=enabled`、`reasoning_effort=high`，每次最多输出 8192 token。可用对应 flag 修改。Harbor 批量入口（run_pilot.py）在宿主侧解析仓库外的凭据文本并只以环境变量传入容器，命令行文本与上传文件不含密钥。

连续工具调用会保留并回传协议需要的 `reasoning_content`，参见 [DeepSeek 思考模式文档](https://api-docs.deepseek.com/guides/thinking_mode/)。

## 运行结果的含义

- `completed`：agent 正常结束，**不表示修复正确**。
- `stopped`：请求、token、轮数或重复调用限制触发。
- `timeout` / `cancelled`：超时或收到可处理的中断信号。
- `error`：启动、模型服务、日志等运行错误。

`result.json` 包含二进制哈希、有效配置及其哈希、任务文本哈希、事件文件路径、停止原因、主 agent 轮数、全队模型请求和用量。`model.missing_usage` 非零时，用量不完整，不能当作已知的零成本。

共享请求/token 预算覆盖主 agent、子 agent、reviewer 和主模型摘要。`--max-model-calls` 计算逻辑模型请求；`--provider-retries 0` 禁止隐藏的 HTTP 重试，让正式试跑的请求次数更容易核对。token 上限在新请求前检查，最后一个请求可能超过上限，它不是精确的金额硬上限。

目前无人值守模式禁止 `--typed-shadow`，因为独立分类器尚未全部接入统一用量计量。也不支持批量 `--resume`；恢复能力需要专门的故障评测。进程被强杀时可能没有最终 JSON，外层 runner 必须把缺失记录识别为异常，不能算通过。

## Harbor

固定 `harbor==0.25.0`，使用 Python 3.12+。建议在 Linux/WSL 的 Docker 环境运行。适配器继承该版本真实的 `BaseInstalledAgent`；安装和命令构造已经有测试。

Linux 下编译并安装工具：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/xcode-linux-amd64 ./cmd/xcode
python3.13 -m venv .cache/harbor-venv
.cache/harbor-venv/bin/python -m pip install -r evals/requirements.txt
```

接上已经核验的原生 Harbor 任务目录后运行：

```bash
PYTHONPATH="$PWD" .cache/harbor-venv/bin/harbor run \
  -p /path/to/verified-tasks \
  -a evals.harbor_agent:XCodeAgent \
  --ak binary_path=bin/xcode-linux-amd64 \
  --ak provider=deepseek \
  --ak credentials_file=/private/deepseek.txt \
  --ak workspace=/app \
  --ak no_delegate=true \
  --ak timeout_sec=1200 \
  --max-retries 0 -k 1 -n 1 -o .eval/harbor
```

`workspace` 必须匹配任务的仓库目录。CLI 的时间上限应比任务的 agent 超时至少短 60 秒，让 runner 有时间导出修改。不要把凭据文件装进镜像、任务目录或 verifier。

能力任务在一次性容器里以 `full-access` 运行；这是隔离容器的执行配置，不适用于宿主安全评测。安全评测必须另外指定权限和审批规则。

适配器会在 CLI 正常结束、预算耗尽或内部超时后导出 `model.patch`，即使 CLI 返回非零也继续交给评分器。文件位于 `/logs/agent/model.patch`。它包含修改、删除、新文件和二进制变动，不改变原 Git 暂存区；排除根目录 `.env` 和 `.xcode-state`。

**Harbor 适配器不会自动把任意数据集变成可信评测。** 每套任务还必须满足：

1. 准备固定 commit 的干净 Git 仓库，清理能泄漏修复答案的历史和引用。
2. 明确配置独立 verifier；只安装 agent 适配器不会自动获得评分隔离。
3. 将候选 patch 作为 artifact 交给 verifier，在新环境应用，随后运行受保护的官方验收测试。
4. 明确处理允许修改的文件；不接受通过修改验收测试绕过评分。
5. 先跑参考修改和空修改，分别应通过、失败。基础设施错误单独标记，不能归为通过。
6. 保存任务来源、版本、镜像 digest 和所有失败；禁止只重跑失败后挑最好成绩。

参考：[Harbor 自定义 agent](https://docs.harborframework.com/agents/custom-agents)、[独立 verifier](https://docs.harborframework.com/tasks/separate-verifier)。

## 2026-11-08 已执行的检查

- 当前 Windows 环境的全量 Go 测试通过，上次权限错误未复现。
- `go vet ./...` 通过；WSL Docker 的 Go 1.26.5 Linux 环境中，`go test -race ./...` 全量通过。
- 协议模拟验证：读/改/新建文件、运行测试、contract、审批拒绝、关闭委派、父子共享预算、超时、启动错误、禁用 HTTP 重试、忽略环境配置、DeepSeek 思考状态续传。
- patch 导出后可在另一份干净 Git 仓库重放，包括新文件、删除和二进制文件；原暂存区保持不变。
- Harbor 0.25 适配器测试通过；密钥只作为进程环境传入，不放进命令和上传文件。
- Linux 静态二进制在无网络的 Alpine Docker 容器里完成 demo 读文件。
- 使用指定 DeepSeek 文本配置，`deepseek-flash` 在启用思考的情况下完成一次真实模型试跑：9 次请求、输入 26486、输出 1321、合计 27807 token，无缺失用量；修复 Add 并新建 Double 实现。在另一个干净目录运行独立验收测试通过。

这次真实试跑是手工小任务，**只证明链路，不是公开仓库能力分数**。本地产物位于 `.eval/deepseek-stage0/`，不提交轨迹或密钥。

## 固定 5 道公开题的真实试跑

题目与预算固定在 `evals/pilot-five.json`：gRPC-Go、go-zero、Express、Flask、Requests 各一道。选择规则在运行前确定，后续不能因为做不出来而换题。数据、官方评分代码、验收脚本均固定版本；镜像使用 digest，不依赖会变化的 latest 标签。

这 5 道题只是链路试跑。它们不足以判断工业级能力，也不能和完整 SWE-bench / Multi-SWE-bench 榜单直接比。正式对比还需要更大的保留集、相同资源预算、基线 agent 和重复试验。

在 Linux/WSL 的 Python 3.13 环境安装 `evals/requirements-pilot.txt`。Docker 必须提供 Compose 和 Buildx 插件。下面以项目内 `.cache/harbor-linux` 环境为例：

```bash
export PYTHONPATH="$PWD"
# 本机的插件装在项目目录；使用系统插件时不必设置这一项。
export DOCKER_CONFIG="$PWD/.cache/docker-config"
.cache/harbor-linux/bin/python evals/fetch_pilot.py
.cache/harbor-linux/bin/python evals/prepare_pilot.py --output .eval/pilot-rerun
mkdir -p .eval/pilot-rerun/build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o .eval/pilot-rerun/build/xcode-linux-amd64 ./cmd/xcode
.cache/harbor-linux/bin/harbor run -p .eval/pilot-rerun/tasks -a oracle -n 2 \
  --max-retries 0 -o .eval/pilot-rerun/harbor --job-name gold-v5
.cache/harbor-linux/bin/python evals/run_pilot.py empty --run-root .eval/pilot-rerun
.cache/harbor-linux/bin/python evals/run_pilot.py agent --run-root .eval/pilot-rerun --credentials-file /private/deepseek.txt
.cache/harbor-linux/bin/python evals/run_pilot.py report --run-root .eval/pilot-rerun
```

`fetch_pilot.py --skip-docker --proxy http://host:port` 可先单独下载、校验公开数据；不读取模型密钥。若任务已经运行，保留原始目录，使用新的 job 名称进行基础设施调试；不得覆盖历史结果或重跑模型后挑最好的一次。

执行分为三步：

1. 参考补丁通过，空补丁失败，才允许该题调用模型。评分错误不算“空补丁失败”。
2. agent 容器中清除答案文件、隐藏测试补丁和 Git 历史，仅保留问题与初始代码。只开放模型 API 网络。
3. 在另一个无外网的全新容器中应用候选生产代码修改，运行受保护的官方验收脚本和固定版本的官方评分器。候选测试修改保留供审查，不参与替换官方验收测试。

**Harbor 0.25 的两个约定已经在这次对接中验证：** 独立 verifier 的自定义镜像需要自带 `/tests`；声明的候选补丁会恢复到 source 路径 `/logs/agent/model.patch`，destination 只控制宿主保存位置。这些路径错误会导致评分基础设施错误，不能误报为模型没修好。

容器分配 2 核，同时设置 `GOMAXPROCS=2`、`GOFLAGS=-p=2`。官方旧 Go 镜像不会自动识别 CPU 配额，按宿主的 32 核并发会触发无关计时测试失败。这项设置同样用于 agent 和 verifier；不删改测试、参考补丁或官方评分逻辑。

第一轮已完成，见 [实际结果与问题分析](PILOT_REPORT.md)：原冻结规则 1/5；发现测试文件规则误伤后，对同一批补丁统一复核，2 道修复通过，新增模型调用 0 次。原始分数没有覆盖。gRPC 因过期测试证书和本机缺失硬件信息无法校准，仍保留在总表里。

后续生成任务默认使用 `protected-upstream` 规则；`--test-policy reject-existing` 可重现旧规则。旧任务目录一旦有运行记录，生成器会拒绝覆盖，必须使用新的 `--output`。上面的示例使用新目录，避免改动这次证据。复现旧题时仍会遇到 gRPC 的环境问题；扩展新集合前应先完成环境体检，再冻结选题。

报告生成在 `.eval/pilot-five/report.md` 和 `report.json`。总数始终是 5，道题未运行或环境出错不会被从分母删掉。逐题同时保留参考对照、空补丁对照、模型轨迹、最终补丁、独立测试输出、请求数和 token；费用未核算时保留空值，不填零。早期校准过程中的环境错误也保留在 `.eval/pilot-five/harbor/`。

## 下一步

1. 跑完固定 5 道真实任务的正反对照和 XCode，按报告定位失败原因。
2. 先修复暴露的基础能力与评测基础设施问题，保留原始成绩，不用同一份小题集反复调到满分后宣称能力提高。
3. 扩展到 30 题开发集，对比 XCode 单智能体、多智能体和同模型的 mini-swe-agent。
4. 另建 100 题验收集，再补真实进程强杀、安全和长任务套件。

现有 `xcode eval` / `eval-fault` 暂时保留用于历史回归。其成功率分母、简化任务和比较阈值不能作为新的正式能力门禁。

## 运行时修复后的独立验证

`evals/runtime-two.json` 在试跑前固定了两道未使用过的题：go-zero 1969、Express 3695。保持同一个 DeepSeek 模型和每题 20 万 token、100 请求、20 分钟预算，使用 `bounded` / 6000 估算 token 与运行时预算提示。产物单独存于 `.eval/runtime-fix/`，不会覆盖第一轮。

生成与运行器支持 `--manifest`；运行器同时用 `--run-root` 指定输出目录。第一轮清单没有声明上下文策略时，运行器仍明确传 `none` / 12000，避免因为适配器默认值改变而悄悄改变旧实验的配置。

这两题用于验证修复后的链路能否工作。它们与第一轮不是同一组题，不能据此计算能力提升百分比。

实际结果见 [运行时修复报告](RUNTIME_FIX_REPORT.md)。首版新题试跑 0/2；补齐定位思路保留和实现进度提醒后，在同两题上的开发回归为 2/2。两轮均保留，预算与模型未变，后者不作未见题成绩。

## 2026-11-09 未见题九题轮

收尾修复(最后请求禁用工具 + 验证后催收尾)之后的第一个未见题集合。12 道候选按预定规则选出并冻结,金/空补丁校准排除 3 道(axios 两道官方测试依赖真实网络,django 一道空补丁意外通过),9 道进入模型运行:**4 道通过独立验收**,九道全部以 completed 干净结束,两道通过题在 7.1 万/9.5 万 token 提前收尾。失败主因回到"探索耗尽预算、没有生产修改"(4/5 道失败题)。冻结清单 `evals/unseen-ten.json`,完整分析见 [未见题轮报告](UNSEEN_TEN_REPORT.md)。

## 2026-11-09 未见题第二轮(round 2)

探索门禁的验证轮。10 道冻结题,公平完成的 6 道中 5 道通过(dayjs×3、sympy×2),全部早编辑、干净收尾;sympy 12489 暴露了门禁的执行侧漏洞——Provider"记得"未声明工具并继续调用,已修复为响应级剪除(`99bdd81`)。cli 三道因 DeepSeek 余额耗尽中断、django 一道补丁导出丢失,均保留为基础设施事件。完整分析见 [第二轮报告](UNSEEN_R2_REPORT.md)。

## 2026-11-10 第三轮:多智能体首验(round 3)

10 道新冻结题、委派开放、共享预算。**核心发现:10 道里 7 道 delegate 调用为零——"开放委派"不等于"使用委派",本轮实际不构成多智能体对照**;真正的 3 道多智能体运行(dayjs)1 过 2 败。过程中根治两个旧问题:capture 脚本的 Python 3.6 兼容(轮 2 django-9296 的追溯根因)、单条非法流式工具调用杀死整轮运行。完整分析见 [第三轮报告](UNSEEN_R3_REPORT.md)。

## A/B/C 消融运行

按交接文档第三节执行。预检、格子清单和 Harbor 适配器都从项目的 `.xcode/config` 读取 `developer`、`reviewer` 及对应的模型槽。密钥仅经进程环境传给 XCode。三个格子的 agent 都使用两模型端点的出网域名，独立 verifier 保持离线。[交接文档](../HANDOVER.md)

启动或续跑时用 `>>` 追加 `.eval/ablation-driver.log`。续跑核对现有清单和二进制快照，并只把缺少 Harbor `result.json` 的题交给新 job。

阶段预算版用 `sh evals/drive_ablation.sh --staged`，结果落在 `.eval/ablation/staged/`。正式开跑前可用 `--staged --pressure-task <冻结题 ID> --pressure-run <新编号>` 只跑 C 格的一道题，沿用正式预算和判分，结果落在 `.eval/ablation/staged-pressure/`。A/B/C 每题使用 50 万 token、100 次模型请求和 20 分钟运行上限，并使用同一可见功能检查。失败时最多返修一次，预留总 token 的 25% 和请求的 10%。功能通过后 B/C 做静态审查，独立 verifier 仍使用冻结题自带的判分。

## 结果汇总与原始数据导出

`evals/EVAL_RESULTS.md` 由脚本生成，逐个 trial 读取 Harbor 的 `result.json`，不手工填数：

```bash
python evals/export_results.py --summary-only   # 只刷新汇总表
python evals/export_results.py                  # 汇总表 + 去重打包
python evals/export_results.py --check <zip>    # 校验包内无个人路径、无长密钥
```

判定分三态：`pass` / `fail` 来自判分器的 `verifier_result.rewards.reward`；`no verdict` 表示没有任何 attempt 走到判分器（异常中断），这类题移出分母单独计数，避免把供应商故障算成模型能力。表里另列 `cut short`（预算耗尽或报错但仍被判分）与 `empty patch`（判为失败但补丁为空），提醒读者这两类失败混合了预算因素。

打包时剔除可复现的二进制（33 份共 438 MB），并对 1 MB 以上的重复文件做内容去重——1.39 GB 的 `native.zip` 其实是同一份 5 MB 文件的 277 次拷贝。小文件一律保留原名，否则所有空补丁会因哈希相同而丢失路径。2.1 GB 的 `.eval/` 因此压到约 33 MB。个人账号名与仓库绝对路径在打包时替换为 `user` 和 `<repo>`。
