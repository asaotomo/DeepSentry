# 自适应运行与桥接媒体补齐

本轮基于已有 runtime v3、事件/检查点、工具中间件、上下文压缩、共享线索板与持久化 outbox 扩展，未替换模型 SDK 或重新运行已完成的工具。

## 执行预算

默认启用 `execution_budget`。原 `max_steps: 30` 和 `subagent_max_steps: 15` 变为初始规划窗口，有新的成功工具结果就以 15 步为单位续期。模型始终可以主动 finish；不会为了用完步数继续任务。

主 Agent 默认最多 180 步，单个子 Agent 最多 90 步，主/子 Agent 共用 300 次推理轮次。并发扣减受锁保护，给主 Agent 留出两轮收尾额度。连续八轮没有新工具输出就暂停；错误、重复输出、TODO、记忆操作和加载 Skill 不续期。保留既有循环检测、权限确认、用户停止与聊天超时机制。

暂停保存检查点，聊天中直接发送“继续”沿用当前会话。续接是一次新运行，重新获得预算，不自动重放上次运行。子 Agent 的自适应预算耗尽通过类型化 `SubAgentIncompleteError` 返回部分证据，避免并行汇总误记为成功完成。

可在 `config.example.yaml` 查看完整配置；`execution_budget.enabled: false` 恢复固定步数行为。聊天默认 1800 秒的任务超时仍生效，可通过 `chat.task_timeout_sec` 调整。多轮长任务应保存中间产物后续接。

当前“进展”是成功工具输出去重的工程代理指标，不是语义验收：不同时间戳可能被视为新结果；同一结果中的成功与失败也受工具回执质量影响。共享额度统计主/子规划轮次，不统计供应商重试、工具内部模型调用和风险复核模型调用，因此不是精确 token/费用上限。没有宣称实现无限自治或通用的“最新架构”。

## 媒体补齐

- 企业微信应用：验证原有加密签名/接收方后解析 image、file、voice，通过当前应用 token 与 MediaId 下载；平台识别文本优先；JSON 业务错误不会被当作文件交给 Agent。
- OneBot：解析数组消息段及 CQ 字符串中的图片、语音、文件；处理群上传/离线文件通知。资源 ID 由鉴权桥接 API 解析，支持 HTTPS 地址、大小受限的 Base64 回执，或显式配置的 `media_root` 共享目录。共享目录使用目录句柄限定读取，拒绝越界和非普通文件。
- OneBot 发送图片使用标准图片消息段；普通文件使用 NapCat 兼容的 `upload_private_file` / `upload_group_file` 扩展。纯 OneBot 11 不定义通用文件上传，其他实现需兼容这些扩展。仅返回本地路径的桥接器需共享目录，或启用其 Base64 资源回传功能。
- 自定义微信桥接：保留原 HTTPS 入站 attachments 协议，出站发送 Base64 attachments；桥接器必须实际实现该协议并返回 `media_accepted: true`，不能仅返回文字接口的 HTTP 200。未确认支持时保留失败记录，不声称文件送达。
- 用户、群白名单验证仍在下载之前。白名单群允许直接发送附件；普通无附件群消息仍按既有指令规则处理。

所有出站文件沿用持久化 outbox，支持 `/delivery`、`/retry`。通用单文件 20 MiB、入站总计 40 MiB 和最多八附件限制保持不变。桥接器的真实账号权限、文件扩展兼容性、语音编码和客户端展示需要联调，不会因本地模拟测试通过就视为真实平台已验收。

## 对当前 Agent 工程实践的取舍

截至本轮检索，2026 年的一手实践强调持久化执行、可验证的交接、执行与评估分离，以及用实测收益选择编排复杂度，而非移除所有执行边界。

- [Anthropic：长任务应用 Harness（2026-03-24）](https://www.anthropic.com/engineering/harness-design-long-running-apps)：分解任务、明确完成标准、外部评估与结构化交接。本项目保留现有隔离子任务与证据板，并改进未完成状态；尚未增加通用独立 evaluator 验收器。
- [Anthropic：Managed Agents（2026-04-08）](https://www.anthropic.com/engineering/managed-agents)：模型执行、工具环境与持久化会话应解耦。本项目媒体解析/下载、Agent 执行与 outbox 投递分离，执行完成和文件送达分别记录。
- [OneBot 11 消息段](https://github.com/botuniverse/onebot-11/blob/master/message/segment.md)、[NapCat 文件实现](https://github.com/NapNeko/NapCatQQ/blob/main/packages/napcat-onebot/action/file/GetFile.ts)、[NapCat 上传实现](https://github.com/NapNeko/NapCatQQ/blob/main/packages/napcat-onebot/action/go-cqhttp/UploadPrivateFile.ts)：按协议适配，区分标准能力和扩展。

入站附件预取已实现：任务入队后立即下载到 `media/prefetch/`，排队期间短时签名 URL 过期不再丢附件。后续架构工作应以真实任务评测驱动：针对具体任务定义验收器、精确费用计量、跨进程租约与后台长任务调度。此次没有以提示词改动冒充这些能力已经实现。

验证：全量 `go test ./...` 通过；`go test -race ./internal/chat ./internal/harness ./internal/config ./cmd` 通过；最终媒体目录读取和消息段兼容调整后重跑相关测试通过，`git diff --check` 通过。测试不调用真实 LLM、不向真实聊天账号发送消息。
