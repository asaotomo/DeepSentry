---
name: fofamap
description: >-
  通过已连接的 FofaMap MCP 做 FOFA 语法校验、规则指纹、资产搜索、主机画像、统计、
  导出和授权 Nuclei 扫描。用户提到 FOFA、测绘、暴露面、公网资产、app=、icon_hash、
  致远/OA/VPN、导出 CSV 时必须先 load_skill 本 skill。不要用 curl 打 FOFA API。
license: Apache-2.0
---

# FofaMap MCP

FofaMap 已出现在本会话工具列表中就等于已经连通。工具名以本会话前缀为准
（常见 `fofamap__fofa_search`）。这是 FofaMap **MCP v2.0.1**（`mcp_server.py`
的 15 个工具），不是 ClawHub 市场里那个跑 `scripts/fofa_recon.py` 的 Skill 包装。
禁止 execute `python3 scripts/fofa_recon.py`，禁止 curl FOFA API，禁止把 API Key
写进报告。

## 工作流

1. `fofa_account` + `fofa_fields`。看 vip_level：注册用户无 Host API；个人/教育账户无 stats API。
2. 用户点名产品、OA、VPN、中间件、摄像头、CMS 时，**先** `fofa_rules`，原样使用返回的 `query`。空 keyword 列出内置目录，不耗额度。禁止编造 `app=`。
3. 耗额度查询前 `fofa_validate_query`。语法不对先修，再 `fofa_search`。
4. 第一页用最小字段集和 size。翻页把返回的 `next_cursor` 原样传给 `fofa_search_next` 的 `cursor`（DeepSentry 也会把 `next_cursor` 映射成 `cursor`）。
5. `fofa_host_profile` / `fofa_stats` 必须先确认账户有对应 API。
6. 大批量结果用 `fofa_export`，再 `fofa_job_status` 或 `mcp_resource action=read uri=fofamap://jobs/{id}` 取本地绝对路径。不要把大表贴进对话。
7. 宽泛中英文发现用 `fofa_agent_run`。组织官网结果保留 `website_candidates` 的 `corroborated` / `observed` / `candidate`，不要拍板成已确认归属。
8. **只有**用户明确要求对已授权范围做主动扫描时才 `nuclei_plan`。核对目标、模板、severity 后，把一次性 `plan_id` + `approval_token` 原样交给 `nuclei_execute`。不得跳过 plan，不得复用令牌。

## 参数兼容

`fofa_export` 和 `nuclei_plan` 的 MCP schema 需要嵌套 `request` 对象。可以：

```json
{"action":"tool","tool_name":"fofa_export","tool_args":{"query":"domain=\"example.com\"","format":"jsonl"}}
{"action":"tool","tool_name":"nuclei_plan","tool_args":{"targets":"https://example.com"}}
```

DeepSentry 会把扁平参数包进 `request`。`fields` / `targets` 可传 JSON 数组或逗号分隔。

## 安全

- 搜索、画像、统计是被动情报，仍要遵守用户授权范围。
- 认证失败、额度耗尽、超时不是空结果。
- FOFA 命中 ≠ 漏洞，也 ≠ 法律上的资产归属。
- 不要把 `FOFA_API_KEY`、approval token 写入报告或记忆。
