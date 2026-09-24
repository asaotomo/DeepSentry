---
name: awd
description: AWD 防守工作流 — 服务可用性、Webshell 排查、日志审计、flag 线索扫描与修复建议
license: Apache-2.0
---

# AWD Skill

## 何时使用

- 用户提到 AWD、攻防赛、防守、靶机服务保活、被打、Webshell、flag 泄露。
- 需要在比赛授权目标上做低风险巡检、证据整理和处置建议。

## 防守优先级

1. 先确认比赛授权范围、目标名、服务 URL/端口、预期状态码和关键文本；多目标先调用 `fleet_inventory` 核对匹配数量。
2. 服务可用性：用 `awd_service_check` 并发检查 HTTP/TCP。HTTP 4xx 是 WARN，5xx 是 DOWN；带认证或自定义健康页时传 `expected_status` / `contains`。TCP OPEN 只说明端口可连，不能证明应用正常。
3. 暴露面：在指定单目标上用 `port_listen`、`net_connections`、`process_list` 建立基线；多目标相同只读巡检用 `fleet_exec`。Windows 与 Linux 必须分组，分别使用对应命令和路径语法。
4. Webshell：委派 `webshell-hunter`，结合 `file_ident`、`file_strings`、最近修改文件；日志委派 `log-analyst`，保留时间、路径、进程和连接证据。
5. Flag/敏感线索：用 `flag_scan` 找明文 flag、token、异常备份，不在报告中暴露完整密钥。
6. 处置前明确目标选择器、匹配数量、受影响服务和回滚路径；按运行器风险确认执行，先少量目标验证，再扩展到同组，最后逐服务复验。

## 推荐工具

```json
{"action":"tool","tool_name":"awd_service_check","tool_args":{"targets":"http://127.0.0.1:8080,127.0.0.1:22","timeout":"3"}}
{"action":"tool","tool_name":"fleet_inventory","tool_args":{"selector":"tag:awd,protocol:ssh"}}
{"action":"tool","tool_name":"flag_scan","tool_args":{"root":"/var/www","limit":"100"}}
{"action":"task","task_name":"webshell-hunter","task_prompt":"检查 /var/www 下可疑 Webshell，不要删除文件"}
```

## 输出格式

```text
## AWD 防守报告
### 服务状态
每项写明目标、URL/端口、预期值、实测状态、延迟和检查时间。
### 高风险发现
### 入侵/后门证据
### Flag/敏感信息暴露
### 建议处置队列
每项写明目标范围、动作、影响、复验结果或阻塞原因。
```

## 边界

- 不自动攻击其他队伍，不批量利用漏洞。
- 不直接删除证据文件；先建议隔离/备份路径。
