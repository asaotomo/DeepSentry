# DeepSentry × FofaMap MCP 适配

DeepSentry v2.0.3 按 FofaMap v2.0.1 的公开 MCP 契约适配 15 个 Tools 和 4 个 Resources。DeepSentry 不内置、复制或代理 FOFA 密钥；FofaMap 仍负责账户认证、额度与查询结果，DeepSentry 负责工具发现、任务路由、超时和审批边界。

## 安装与配置

在 FofaMap 项目目录创建独立环境并完成官方初始化：

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -e .
.venv/bin/fofamap init
```

Windows 将 `.venv/bin/python` 换成 `.venv\Scripts\python.exe`。初始化优先把 FOFA API Key 保存到系统钥匙串；不要把真实 Key 写进 DeepSentry 配置、截图、日志或 Git。

在 DeepSentry `config.yaml` 中使用绝对路径：

```yaml
mcp_server_configs:
  - name: fofamap
    type: stdio
    command: "/absolute/path/FofaMap/.venv/bin/python"
    args: ["/absolute/path/FofaMap/mcp_server.py"]
    startup_timeout_sec: 30
    tool_timeout_sec: 0
    required: false
    disabled: false
```

绝对路径可避免 TUI、GUI 或服务进程没有继承终端 `PATH` 时出现 `spawn ... ENOENT`。配置后重启 DeepSentry，执行 `/mcp status`，应看到 `fofamap` 已连接且发现 15 个工具。

也可以先独立启动本机 Streamable HTTP：

```bash
.venv/bin/fofamap-mcp --transport streamable-http --host 127.0.0.1 --port 8001
```

然后配置 `type: streamable_http` 与服务实际输出的 MCP URL。只建议在回环地址无鉴权运行；暴露到远端时必须使用 HTTPS 和 FofaMap 支持的鉴权边界。

## 工作流

| 目的 | 顺序 |
| --- | --- |
| 账户和字段预检 | `fofa_account` → `fofa_fields` |
| 产品/OA/VPN 指纹 | `fofa_rules`，原样使用返回的 `app=` 规则 |
| 普通资产查询 | `fofa_validate_query` → `fofa_search` |
| 继续翻页 | 将 `next_cursor` 原样传给 `fofa_search_next` |
| 大结果集 | `fofa_export`，核对绝对路径和导出条数 |
| 图标/画像/聚合 | `fofa_icon_search`、`fofa_host_profile`、`fofa_stats` |
| 宽泛自然语言测绘 | `fofa_agent_run` |
| 主动扫描 | `nuclei_plan` → 用户确认 → 原样传一次性令牌给 `nuclei_execute` |

FOFA 命中是网络空间测绘线索，不等同于已经验证的漏洞。报告必须区分被动查询结果、推断和经过授权的主动验证证据。

`fofa_export` 与 `nuclei_plan` 的 MCP schema 使用嵌套 `request`。Agent 可以传完整 JSON，也可以扁平传 `query`/`targets`/`fields`；DeepSentry 会在调用前包进 `request`。`fields` 和 `targets` 支持 JSON 数组或逗号分隔。`fofa_search_next` 的官方参数名是 `cursor`，若模型误传 `next_cursor` 也会自动映射。

Resources：`fofamap://fields`、`fofamap://rules`、`fofamap://account`、`fofamap://jobs/{job_id}` 可通过 `/mcp resources fofamap` 和 `/mcp read fofamap <uri>` 访问。资产测绘任务应先 `load_skill("fofamap")`。

## 风险与超时

| 工具 | DeepSentry 风险 |
| --- | --- |
| `fofa_*` 查询、规则、账户、统计和任务状态 | 低风险，只读 |
| `fofa_export` | 中风险，在控制端写文件 |
| `nuclei_plan` | 中风险，只生成范围受限的计划和一次性令牌 |
| `nuclei_execute` | 高风险，对授权目标发起主动扫描 |

`tool_timeout_sec: 0` 或省略时，搜索/导出类默认 120 秒、`fofa_agent_run` 300 秒、`nuclei_execute` 600 秒，其他工具使用通用 60 秒。显式配置始终优先。

## Resources 与排障

FofaMap 的 `fofamap://fields`、`fofamap://rules`、`fofamap://account`、`fofamap://jobs/{job_id}` 可通过 `/mcp resources fofamap` 和 `/mcp read fofamap <uri>` 访问。

- 连接失败：在同一终端直接运行配置中的 Python 与 `mcp_server.py`，检查依赖和绝对路径。
- 认证失败：重新运行 `fofamap init`，检查系统钥匙串中的 FOFA Key；不要在聊天中粘贴真实 Key。
- 工具不是 15 个：确认使用 FofaMap v2.0.1，并执行 `/mcp reconnect fofamap` 重新发现能力。
- 查询失败：先运行 `fofa_account`、`fofa_fields` 和 `fofa_validate_query`，根据返回的账户权限、字段和语法修正。
- 扫描被确认面板拦截：这是预期安全边界；核对计划中的授权目标、模板与影响后再批准。

上游项目与契约以 [FofaMap v2.0.1](https://github.com/asaotomo/FofaMap/tree/v2.0.1) 为准。
