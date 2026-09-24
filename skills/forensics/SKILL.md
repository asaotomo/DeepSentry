---
name: forensics
description: 文件取证 — 魔数识别、strings 提取、gzip 日志解压，全部 Go 原生无需 file/strings/zcat
license: Apache-2.0
---

# 文件取证 Skill

## 何时使用

- 分析可疑二进制、Webshell、恶意样本
- 读取 `.gz` 压缩日志（auth.log、nginx access.log 等）
- 目标系统无 `file`、`strings`、`zcat`、`gunzip` 命令

## 工具选用

| 场景 | 工具 | 示例 |
|------|------|------|
| 文件是什么类型 | `file_ident` | `{"tool_name":"file_ident","tool_args":{"path":"/tmp/suspicious"}}` |
| 提取硬编码字符串/IP/URL | `file_strings` | `{"tool_name":"file_strings","tool_args":{"path":"/var/www/x.php","pattern":"eval\|base64"}}` |
| 读 gzip 日志 | `read_gzip` | `{"tool_name":"read_gzip","tool_args":{"path":"/var/log/auth.log.1.gz","lines":"100"}}` |
| 不确定是否 gzip | `read_log` | `{"tool_name":"read_log","tool_args":{"path":"/var/log/syslog","pattern":"Failed"}}` |
| 文件哈希 | `file_hash` | `{"tool_name":"file_hash","tool_args":{"path":"/bin/busybox"}}` |

## 推荐流程

1. `file_ident` — 确认文件类型（ELF/PHP/GZIP/文本）
2. 若为 gzip 日志 → `read_log` 或 `read_gzip` + pattern 过滤
3. 若为二进制/可疑脚本 → `file_strings` + pattern
4. `file_hash` — 留存 IOC

## 限制

- 单文件读取上限 5MB（压缩），解压上限 20MB
- strings 扫描上限 2MB
- 禁止读取 config.yaml / reports/ 等受保护路径
