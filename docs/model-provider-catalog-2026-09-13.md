# 模型提供商目录核对（2026-09-13）

本次更新初始化向导和运行时共享目录，不修改用户 config.yaml 中已指定的模型、密钥和地址。新建配置使用下列默认值，模型输入框可按 Tab 查看当前提供商候选项，也可直接输入控制台允许的 ID。菜单“图片理解”只表示图片输入能力，不表示本程序接入了厂商所有音视频生成 API。

| 提供商 | 默认 Model ID | Base URL / 协议 | 核对来源 |
|---|---|---|---|
| OpenAI | gpt-6-astra | https://api.openai.com/v1 / Responses | [模型](https://developers.openai.com/api/docs/models)、[迁移参数](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-6-astra) |
| Anthropic | claude-fable-5-1 | https://api.anthropic.com/v1 / Messages | [模型目录](https://platform.claude.com/docs/en/models/overview) |
| Google | gemini-3.8-flash | https://generativelanguage.googleapis.com/v1beta/openai / Chat Completions | [模型](https://ai.google.dev/gemini-api/docs/models)、[兼容接口](https://ai.google.dev/gemini-api/docs/openai) |
| DeepSeek | deepseek-flash | https://api.deepseek.com / Chat Completions | [官方调用说明](https://api-docs.deepseek.com/) |
| 阿里百炼 | qwen3.8-max | https://dashscope.aliyuncs.com/compatible-mode/v1 / Chat Completions | [模型](https://help.aliyun.com/en/model-studio/qwen3-8-max) |
| MiniMax 中国站 | MiniMax-M3 | https://api.minimax.cn/v1 / Chat Completions | [官方兼容接口](https://platform.minimax.cn/docs/api-reference/text-openai-api) |
| 智谱 | glm-5.3-flash | https://open.bigmodel.cn/api/paas/v4 / Chat Completions | [模型与能力](https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash) |
| xAI | grok-4.6 | https://api.x.ai/v1 / Chat Completions | [模型](https://docs.x.ai/developers/models) |
| 百度千帆 Coding Plan | qianfan-code-latest | https://qianfan.baidubce.com/v2/coding / Chat Completions | [官方文档搜索结果核对，正文抓取失败](https://cloud.baidu.com/doc/qianfan/s/imlg0beiu) |
| 火山方舟 Coding Plan | ark-code-latest | https://ark.cn-beijing.volces.com/api/coding/v3 / Chat Completions | [官方调用说明](https://www.volcengine.com/article/37359) |
| 天翼云星辰编程 Token Plan | GLM-5-Pro | https://wishub-x6.ctyun.cn/coding/v1 / Chat Completions | [官方接入说明](https://www.ctyun.cn/document/11061839/11092768) |
| 腾讯 TokenHub | hy4-preview | https://tokenhub.tencentmaas.com/v1 / Chat Completions | [官方发布](https://www.tencent.com/tencent-releases-and-open-sources-tencent-hy4-preview/)；保留原中国站入口，调用指南正文抓取失败 |
| MiMo Token Plan 中国站 | mimo-v2.5 | https://token-plan-cn.xiaomimimo.com/v1 / Chat Completions | [官方入口](https://platform.xiaomimimo.com/docs/en-US/price/tokenplan/quick-access) 重定向后未取得正文，本次保留原预设，不能视作重新验证成功 |
| Ollama | 用户已安装模型 | http://localhost:11434/v1 / Chat Completions | [兼容接口](https://docs.ollama.com/api/openai-compatibility) |
| LM Studio | 用户已加载模型 | http://localhost:1234/v1 / Chat Completions | [兼容接口](https://lmstudio.ai/docs/developer/openai-compat) |

## 调用修正

- Claude 新建配置默认 Fable 5.1；仍可选择 Opus 5、Sonnet 5 和 Haiku 4.5。
- DeepSeek 官方已将旧 deepseek-v4-flash 请求转到 V4.1 Flash，因此旧 API ID 的图片能力同步修正；候选列表推荐 deepseek-flash。
- OpenAI 新建配置默认 Responses，移除 Astra 不支持的 temperature。明确使用 Chat Completions 时，Astra 使用 max_completion_tokens 并不发送原生工具定义。
- Responses / Messages 地址按协议补齐，不再出现 chat/completions/v1/responses 等重复路径；自定义网关前缀保留。
- OpenAI、Claude、Gemini 等型号名称、菜单图片标记来自同一份预设；本地模型不提供虚构的“已安装最新模型”列表。

## 验证范围

接口验证使用本地模拟 HTTP 服务，覆盖端点、模型 ID、参数和响应解析，未使用生产 API Key 验证账户可用性或额度。套餐密钥和普通 API 密钥不可假定互通；最终可用模型以用户平台控制台为准。

当前 Responses 和 Anthropic 适配使用已有文本/图片与 Agent 动作协议；Responses 原生 function-call 往返、流式事件及厂商所有专用接口并未在本次扩展。保留现有 Chat Completions 原生工具链，不将目录更新表述为这些能力已经全部实现。
