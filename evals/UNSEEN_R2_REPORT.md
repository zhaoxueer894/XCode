# 未见题第二轮(round 2):2026-11-09

验证探索门禁(commit `e65cfff`)的第一轮未见题对照。十个任务、四个上一轮环境合格的仓库(cli/dayjs/sympy/django),规则先冻结后执行;运行二进制含门禁,SHA-256 `c9cf7d8df052…`(与收尾轮 `aaf9ec76` 的差异仅为门禁)。金/空补丁校准在最终任务目录上完成(gold-v3/empty-v1),django 10097 因金补丁不通过排除,不换题。

## 结果

| 题目 | 通过 | 请求 | token | 首次生产修改轮 | 结束方式 |
|---|---|---:|---:|---:|---|
| dayjs 379 | **通过** | 27 | 199451 | 20 | completed |
| dayjs 556 | **通过** | 21 | 155410 | 7 | completed |
| dayjs 569 | **通过** | 26 | 203003 | 18 | completed |
| sympy 12419 | **通过** | 26 | 203570 | 15 | completed |
| sympy 12481 | **通过** | 29 | 205698 | 10 | completed |
| sympy 12489 | 未通过 | 25 | 205278 | 无 | completed |
| cli 352 | 供应商中断 | 12 | 79154 | 无 | 402 余额耗尽 |
| cli 362 | 供应商中断 | 1 | — | 无 | 402 余额耗尽 |
| cli 402 | 供应商中断 | 1 | — | 无 | 402 余额耗尽 |
| django 9296 | 基础设施失败 | 21 | 136323 | 6 | 补丁导出丢失 |

**公平完成的 6 道中 5 道通过独立验收**(sympy 12489 未通过);与第一轮 9 道 4 通过相比方向为正,但两轮任务不同,不构成可比的能力百分比。已记录模型请求 163 次、约 117 万 token(用量完整);DeepSeek 账户在 cli 352 第 12 步余额耗尽,cli 三道的补跑判分不作为模型结果,保留原始记录。

## 探索门禁的验证结论

**门禁按设计触发了,但执行侧存在漏洞,已修复。** sympy 12489 的轨迹显示:level-1 通知 4 次、level-2(禁 exec)通知 5 次,声明列表中搜索类工具确实消失——但 Provider"记得"未声明的 exec 并继续调用,Agent 执行时也不校验声明列表,导致 level-2 期间仍执行了 11 次 exec,最终零生产修改烧满预算。该任务结果有效(公平完成),漏洞定性为机制缺陷,修复为在响应进入执行前剪除未声明工具的调用(commit `99bdd81`,含回归测试)。

正面证据:5 道通过题全部早编辑(第 7-20 轮首次生产修改),无一零编辑收尾;dayjs 三道全部通过(第一轮同仓库 318 曾以零编辑失败)。"零编辑归零"的目标在通过题上成立,在 sympy 12489 上因上述漏洞未成立——修复后的运行时将在下一轮重新检验。

## 过程事件(全部留证)

- gold-v1:capture_patch 对 cli 仓库 .gitignore 覆盖的已跟踪路径(.vscode)导出失败(三道 error);修复为 `git add -f` 后 gold-v2(cli 三道)通过。任务目录重建导致 gold-v1 校验和与 empty 不匹配,在最终目录上重跑全量 gold-v3 定稿。
- empty-v1:12 道全部如预期失败。
- xcode-v1:dayjs/sympy/django 七道完成;cli 三道环境启动超时(600 秒)——3 路并发下 compose 经代理做 registry 摘要校验变慢所致,单独串行复跑(xcode-v3)环境正常。
- xcode-v2:并发补跑再次全部环境超时,作废保留;xcode-v3 串行补跑完成,随后账户余额耗尽。
- django 9296:agent 正常完成(21 步、有生产修改),运行后补丁导出失败丢失(原因不可诊断,容器已销毁);harbor_agent 已增加 capture 日志落盘。该题模型已见过,不重跑,计为基础设施失败。

## 界限

10 道小样本、单 agent、单次运行,不作能力百分比宣称。cli 三道与 django 一道实际未获得公平判定:round 2 的有效分母是 6,通过 5。供应商余额与基础设施事件消耗约 25 万 token 与数小时,相关证据目录(xcode-v1/v2/v3、gold-v1/v2/v3)全部保留在 `.eval/unseen-r2/harbor/`。

## 如何查证

- 冻结清单:`evals/unseen-r2-candidates.json`(12 候选)→ `evals/unseen-r2.json`(定稿 10 题)。
- 校准审计:`.eval/unseen-r2/calibration-audit.json`;模型轮:`.eval/unseen-r2/harbor/xcode-v1|v2|v3/`。
- 门禁触发与 exec 泄漏证据:`xcode-v1/sympy__sympy-12489*/agent/events.jsonl`(budget.status 事件含各级通知)。
- 402 中断证据:`xcode-v3/cli__cli-352*/agent/stderr.txt`。
- 运行二进制:门禁版 `c9cf7d8d…`;执行侧修复版 `6ce0d646…`(未参与本轮,供下一轮)。
