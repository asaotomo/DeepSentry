---
name: awd-plus
description: AWD-plus 综合运营工作流 — 多服务态势、低风险自动化检查、证据链汇总和处置队列
license: Apache-2.0
---

# AWD-plus Skill

## 何时使用

- 用户提到 AWD-plus、多服务、多队伍、多目标防守、态势运营、比赛值守。
- 需要把服务可用性、日志、文件、网络连接和 flag 暴露统一成证据链。

## 工作方式

1. 先用 `todo` 建立 3-7 项检查清单，避免遗漏服务、日志和文件线索。
2. 多目标环境先 `fleet_inventory`，核对目标名、标签、协议和匹配数量。selector 用逗号取交集、`|` 取并集；按队伍、服务、操作系统和协议分组。不要给混合 Windows/Linux 或 SSH/FTP 目标发送同一命令。
3. 建立“目标 × 服务”矩阵，记录 URL/端口、预期 HTTP 状态/关键文本、上次正常证据。用 `awd_service_check` 并发探活；WARN/DOWN 节点单独复查，不把 TCP OPEN 当作业务正常。
4. 主机面同组只读巡检用 `fleet_exec`，每台独立分析才用 `task` + `target_selector`；Web 面按异常节点调用 `webshell-hunter`，网络/日志线索交对应专项 Agent。
5. 证据面使用 `fleet_file read/download`、`read_log`、`read_gzip`、`proc_socket_map`；多目标下载会自动生成不同本地文件名，报告应保留实际路径和目标对应关系。
6. 处置队列按“服务中断、正在被利用、证据保全、普通配置问题”排序。变更按运行器风险确认执行，先验证少量目标，再逐组扩展；每组复验服务、日志和文件状态。
7. 用户明确要求持续值守时，可先用 `schedule_task plan` 规划周期和 selector；仅在用户要求创建持久任务时使用 `schedule_task add`。周期任务只做可重复的健康检查与异常汇总，避免重复执行修复。

## 推荐委派

```json
{"action":"task","task_name":"awd-defender","task_prompt":"检查当前目标 Web 服务可用性、Webshell 和日志异常"}
{"action":"task","task_name":"network-analyst","task_prompt":"分析异常连接和可疑出站流量"}
```

## 输出格式

```text
## AWD-plus 态势报告
### 当前态势
### 服务/目标矩阵
逐行列目标、服务、预期值、实测值、延迟、异常时间和责任分工。
### 证据链
### 风险排序
### 待确认处置队列
标明 selector、匹配数量、动作、影响范围、复验状态。
```

## 边界

- 聚焦防守运营，不做自动攻击编排。
- 保留证据优先；删除、覆盖、重启服务前必须说明影响并请求确认。
