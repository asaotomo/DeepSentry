---
name: network-recon
description: 网络排查 — 指导 AI 使用 Go 原生内置工具 (BusyBox 模式)，无需目标系统 nmap/ss/tcpdump
license: Apache-2.0
---

# 网络排查 Skill (BusyBox 模式)

## 核心原则

DeepSentry 工具是 **Go 原生实现**，不依赖目标系统的 nmap/ping/ss/netstat/tcpdump/ps。

极简/嵌入式 Linux 只需：
- 内核暴露 `/proc`（几乎所有 Linux 都有）
- SSH + SFTP（DeepSentry 直读文件，连 `cat` 都不需要）

## 工具选用

| 场景 | 工具 | 说明 |
|------|------|------|
| 主机可达 | `ping` | Go TCP 探活，无需 ping 命令 |
| 当前连接 | `net_connections` | 解析 /proc/net/tcp |
| 监听端口 | `port_listen` | /proc LISTEN |
| 路由/ARP | `route_table`, `arp_table` | /proc 解析 |
| 端口扫描 | `nmap_scan` | Go TCP 扫描，无需 nmap 🔴 |
| TCP 探活 | `netcat_probe` | Go net.Dial |
| 抓包替代 | `flow_snapshot` | /proc 快照对比，无需 tcpdump |
| 进程/内存 | `process_list`, `mem_info` | /proc 直读 |

## 远程排查注意

- **目标本机状态**（连接/监听/进程）：用 `net_connections`/`port_listen`/`process_list`（读目标 /proc）
- **从外部分扫描目标端口**：`nmap_scan`/`ping` 从 DeepSentry 控制端发起，需填目标 IP（非 127.0.0.1）
- 无 tcpdump 时：`packet_capture` 自动降级为 `flow_snapshot`

## 示例

```json
{"action":"tool","tool_name":"net_connections","tool_args":{"filter":"established"}}
{"action":"tool","tool_name":"port_listen"}
{"action":"tool","tool_name":"process_list","tool_args":{"limit":"30"}}
{"action":"tool","tool_name":"flow_snapshot","tool_args":{"interval":"3"}}
```
