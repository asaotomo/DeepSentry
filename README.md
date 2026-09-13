<div align="center">

# 🛡️ DeepSentry v2.0.4 Ultimate — 深海哨兵

<h3>"让 AI 成为你的红蓝对抗伙伴与安全运维专家。"</h3>

<p>
  <i>Your AI-powered Security Agent for Local, Remote & Fleet Auditing.</i>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Team-Hx0-red?style=flat-square" alt="Team">
  <img src="https://img.shields.io/badge/Version-v2.0.4%20Ultimate-2f81f7?style=flat-square" alt="Version">
  <img src="https://img.shields.io/badge/Platform-Windows%20%7C%20Linux%20%7C%20macOS-gray?style=flat-square&logo=linux&logoColor=white" alt="Platform">
  <img src="https://img.shields.io/badge/Go-1.26.8+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/AI-Multi--Provider-blueviolet?style=flat-square" alt="AI">
</p>

[一眼看懂](#一眼看懂) • [下载](#下载哪个文件) • [聊天机器人](#聊天机器人接入) • [巡检报告](#网页巡检与-word-报告) • [使用技巧](#v204-使用小技巧) • [案例用法](#典型场景与案例用法) • [快速开始](#5-分钟快速开始)

</div>

DeepSentry 是一个跑在你自己电脑或服务器上的 **AI 安全应急 / 智能运维 Agent**。用中文说清目标，它会规划步骤、调用工具、连本地或远程主机，并留下可审计的报告。

v2.0.4 起也可以：

1. 在 **微信、QQ、企业微信、飞书、钉钉** 里私聊指挥（验证码回原任务，高危操作当场确认）。
2. 对设备 **Web 控制台做网页巡检**，保留真实截图，一句话生成带封面的 **Word / Markdown**。

<img width="1672" height="941" alt="Deepsentry2 0 4海报" src="https://github.com/user-attachments/assets/a04f8117-4856-41b4-b8d8-53ad05b5225e" />

> **授权范围**：只允许在你拥有或已获得明确书面授权的系统上使用。禁止未授权扫描、入侵、破坏、绕过访问控制或任何违法用途。

> **数据会出本机**：任务描述、必要上下文和工具结果会发给你配置的大模型服务。涉密环境请改用受控本地模型，公开报告和截图前先脱敏。漏洞请走 [Security Advisories](SECURITY.md)，不要开公开 Issue。

---

## 目录

- [一眼看懂](#一眼看懂)
- [最新版本亮点](#最新版本亮点)
- [v2.0.4 使用小技巧](#v204-使用小技巧)
- [典型场景与案例用法](#典型场景与案例用法)
- [CTF / AWD / AWD-Plus 能力](#ctf--awd--awd-plus-能力)
- [下载哪个文件](#下载哪个文件)
- [5 分钟快速开始](#5-分钟快速开始)
- [聊天机器人接入](#聊天机器人接入)
- [网页巡检与 Word 报告](#网页巡检与-word-报告)
- [配置文件说明](#配置文件说明)
- [常用运行模式](#常用运行模式)
- [WebShell / 蚁剑 / 非交互环境用法](#webshell--蚁剑--非交互环境用法)
- [TUI 全屏界面用法](#tui-全屏界面用法)
- [内置工具清单](#内置工具清单)
- [多目标 Fleet 用法](#多目标-fleet-用法)
- [长上下文与多 Agent 协作](#长上下文与多-agent-协作)
- [报告、会话与记忆](#报告会话与记忆)
- [外部 MCP 与 Skills 扩展](#外部-mcp-与-skills-扩展)
- [定时任务与多通道通知](#定时任务与多通道通知)
- [从源码构建](#从源码构建)
- [常见问题](#常见问题)
- [安全建议](#安全建议)
- [项目结构](#项目结构)

**建议阅读顺序**

| 你是谁 | 先看 |
| --- | --- |
| 第一次下载 | [下载哪个文件](#下载哪个文件) → [5 分钟快速开始](#5-分钟快速开始) |
| 想用手机指挥 | [聊天机器人接入](#聊天机器人接入) |
| 要网页截图和 Word | [网页巡检与 Word 报告](#网页巡检与-word-报告) |
| 接鹰眼 / FofaMap | [鹰眼 MCP 深度适配](docs/HawkEye-MCP-深度适配.md)、[FofaMap MCP 适配](docs/FofaMap-MCP-适配.md) |
| 查完整参数 / 排错 | [操作手册](docs/操作手册.md)、[常见问题](#常见问题) |
| 报告漏洞 | [SECURITY.md](SECURITY.md) |

遇到使用问题可以开 [GitHub Issues](https://github.com/asaotomo/DeepSentry/issues)。提问时请去掉 API Key、密码、真实客户地址和未脱敏截图。

---

## 一眼看懂

| 你想做什么 | DeepSentry 怎么做 |
| --- | --- |
| 排查一台服务器 | 用中文下任务，自动看系统、资源、进程、端口、登录记录 |
| 用手机指挥 | 扫码绑定微信 / QQ / 飞书等，私聊下发任务，验证码回原对话确认 |
| 网页设备留证 | 接上 Hx0 **鹰眼（HawkEye）** 后，用真实浏览器打开控制台、截图，再出带封面的 Word |
| 查互联网暴露面 | 接上 Hx0 **FofaMap** 后，用自然语言搜资产、套产品规则，不必手写 FOFA 语法 |
| 分析安全事件 | 读日志、筛异常登录、关联可疑进程和网络连接 |
| 多台一起巡检 | 配置主机清单，按名称或标签批量只读检查 |
| 比赛 / 演练辅助 | CTF 初筛、AWD 值守、多靶机协同（必须在授权环境） |
| 要审计留痕 | 每次任务写 Markdown；网页巡检额外出 Word |
| 目标机几乎没工具 | 大量能力在控制端用 Go 实现，不依赖目标机装齐常用命令 |

你需要准备：

1. **一个大模型 API**（DeepSeek / 国产或国际云、或本机 Ollama 等）。没有 Key 跑不起来。
2. **一台你有权检查的机器或网页控制台**。默认先查本机。
3. （可选）**聊天软件**，以及 Hx0 的 **鹰眼 / FofaMap**。前者用来手机指挥；鹰眼负责真实浏览器巡检截图，FofaMap 负责互联网资产测绘。都不是第一步的必需品。

核心能力：中文全屏终端、本地 / SSH / Telnet / FTP / 多主机、73 个内置工具、会话恢复、定时任务、聊天机器人、网页巡检 Word、对 **Hx0 鹰眼 MCP** 与 **FofaMap MCP** 的深度适配。

---

## 最新版本亮点

确认版本：

```bash
./deepsentry --version
# 若你是从源码编译： ./build/deepsentry --version
```

应看到 `DeepSentry v2.0.4 Ultimate (build 日期)`。日期随你下载或自行编译的包变化。

| 你能直接用到的 | 说明 |
| --- | --- |
| 聊天机器人 | 微信、QQ、企业微信、飞书可扫码；钉钉走应用机器人。电脑上 `/connect` 或 `--chat-setup`，纯 SSH/CMD 用 `--chat-setup-cli` |
| 手机指挥 | 私聊直接说任务；发「重启会话」「切换会话」；做到一半停住了就回「继续」 |
| 验证码和文件 | 登录/滑块图先发到聊天，你回复后回到**原来那次巡检**；也可以把 Word 发回群里 |
| 高危确认 | 聊天里选允许本次 / 本会话同类 / 本次会话全部高危 / 拒绝。生产环境不要开无人值守自动批准 |
| 跟终端同生命周期 | 配好通道后启动 DeepSentry 会带上机器人；关掉终端界面，聊天一并停。要 24 小时挂着再用 `--chat` |
| 网页巡检出 Word | 接上鹰眼后打开设备后台，说「生成报告」即可。不用写 JSON，不用安装 pandoc |
| Word 版式 | 封面 + 先结论再细节和建议，截图跟在对应章节后面 |
| 每天自动巡 | 无验证码的固定检查可定时跑；要人眼看验证码的，请从聊天里触发 |
| 鹰眼 MCP 深度适配 | [Hx0 鹰眼](https://hx0studio.com/) 是我们做的浏览器自动化。DeepSentry 按 MCP 1.0.7 的 51 个工具做了点对点适配（登录、截图、验证码辅助、长页续读），不是通用转发 |
| FofaMap MCP 深度适配 | [FofaMap](https://hx0studio.com/) 是我们做的 FOFA 资产测绘客户端。DeepSentry 按 v2.0.1 的 15 个工具做了点对点适配（账户、规则、搜索、翻页、导出），密钥仍留在 FofaMap 里 |
| 视觉模型 | 常见多模态型号会自动识别截图；纯文本模型不会被硬塞图片 |

分步说明：[聊天机器人接入](docs/聊天机器人接入.md)、[终端接入与每日巡检](docs/终端接入与每日巡检.md)。鹰眼和 FofaMap 都是 **Hx0 自家产品**，通过 MCP 接到 DeepSentry；详细契约见 [鹰眼适配](docs/HawkEye-MCP-深度适配.md) 与 [FofaMap 适配](docs/FofaMap-MCP-适配.md)。

---

## v2.0.4 使用小技巧

最有效的提示词不是“帮我看看”，而是一次给清楚四件事：**目标、范围、权限边界、交付物**。

```text
目标：排查这台交换机上联口丢包和高流量。
范围：只看最近 30 分钟，先只读，不修改配置。
证据：保留执行过的命令、关键原始输出和时间。
交付：按“现象→证据→根因→建议”写；证据不够必须写明，不要猜。
```

| 场景 | 推荐技巧 |
| --- | --- |
| 互联网资产 | 接上 FofaMap 后先 `load_skill("fofamap")`，产品名查规则再搜，不要手写 `app=` |
| 真实网页 | 鹰眼已连接时让 Agent 走鹰眼，不要再用内置浏览器工具 |
| 用手机指挥 | 先 `--init` 配好模型，再 `/connect` 或 `--chat-setup` 扫码；无桌面用 `--chat-setup-cli` |
| 网页出 Word | 巡检结束后说「生成报告」；先结论、再详情和建议，截图跟章节走 |
| 聊天验证码 | 先看机器人发来的图，直接回复，不要 `/new` |
| 聊天会话 | 「重启会话」开新对话，「切换会话」按序号恢复；停住了回「继续」 |
| 输入框清空 | `Ctrl+A` 全选后再按删除；`Ctrl+U` 也能一键清空 |
| 日常排障 | 第一轮明确“只读取证”；模型给出根因和最小修改方案后，再单独批准变更与复验 |
| 华为/H3C/锐捷 | 配置 `ssh_device_type` / `telnet_device_type`；先执行完整 `display/show`，确认上下文后再过滤 |
| 输出疑似不完整 | 区分 `projection=filtered`、TUI 折叠和 `output_truncated=true`；只有最后一种是真正字节截断 |
| Fleet 多机 | 先 `fleet_inventory` 确认 selector，再并行采证；修改型动作按目标串行审批 |
| FTP/FTPS | 先列目录、确认远端路径和文件大小，再下载并校验哈希；NAT 后数据通道失败可用 `ftp_data_mode: auto`/`active`，生产环境优先 SFTP 或验证证书的 FTPS |
| 出站代理 | 临时使用 `-proxy` 或 `-socks5`；需持久化再设置 `controller_proxy`，两种命令行代理不可同时使用 |
| 重复高风险确认 | `Y` 只批准当前操作；`A` 按确认面板显示的“目标 + 工具/操作类型”授权本会话同类操作；`S` 允许本次会话所有高危操作。`/new`、切换会话后授权失效 |
| 启动本地 Web 服务 | 要求 Agent 后台启动并记录 PID/端口；服务脱离前台管道后任务可继续，不必为了让 Agent 前进而关服务 |
| 模型不稳定 | 在 `models[]` 配置 fallback；持续 429 时不要无限重试，使用 `--resume` 从 checkpoint 继续 |
| 大日志/证据 | 要求“结论必须引用 artifact、命令和关键输出”，不要让自然语言摘要成为唯一证据 |
| 比赛 10 分钟题 | 启用 `--competition`，限定时间窗和答案格式，最后用 `competition_answer_check` 做幻觉与漏项检查 |
| 自动化/CI | 使用 `--no-tui --json --task`；消费结构化事件，不要解析彩色终端文本 |
| 白色/深色终端 | 保持 `terminal_theme: auto`；老 SSH/tmux 终端探测不准时用 `--theme light` 或 `--theme dark` 临时固定 |
| 高风险操作 | 生产环境不要使用无人值守自动批准（`--batch -y`）；先备份、再最小修改、最后用独立证据复验 |

---

## 典型场景与案例用法

DeepSentry 的核心用法不是记命令，而是把目标、范围和期望结果说清楚。Agent 会自己选择 Shell、内置工具、Fleet 多目标、文件传输、子 Agent 或报告生成流程。

### 1. 日常服务器巡检

适合上线前检查、日常运维、云主机交付验收。

```bash
./deepsentry -c config.yaml --task "检查这台服务器的系统版本、CPU、内存、磁盘、负载、监听端口、最近登录用户和异常进程，最后按风险等级输出巡检报告。"
```

它通常会组合使用：

- `target_health_summary` 查看系统整体状态。
- `mem_info`、`disk_usage`、`process_list` 获取基础资源。
- `port_listen`、`net_connections`、`route_table` 判断网络暴露面。
- `login_audit` 检查登录记录。
- Markdown 报告沉淀结论和证据。

### 2. SSH 登录日志审计

适合排查爆破、撞库、异常来源 IP、可疑登录时间线。

```bash
./deepsentry -c config.yaml --task "审计今天的 SSH 登录日志，统计失败登录 Top IP、成功登录账号、异常时间段和可能的攻击来源，并给出封禁建议。"
```

可进一步要求：

```text
把 auth.log、secure、syslog 中的登录行为合并成时间线，区分失败登录、成功登录、sudo、su、ssh key 登录和异常来源 IP。
```

### 3. WebShell 和后门排查

适合 Web 目录被篡改、可疑 PHP/JSP/ASP 文件排查、应急响应初筛。

```bash
./deepsentry -c config.yaml --task "检查 /var/www/html 是否存在疑似 WebShell、混淆脚本、最近新增文件和可疑外连，输出文件路径、命中原因和处置建议。"
```

它可以结合：

- `secret_scan` 查找敏感配置、密钥和可疑片段。
- `file_ident`、`file_strings` 判断文件类型和可疑字符串。
- `read_log` 分析访问日志和错误日志。
- `process_list`、`net_connections` 查找 Web 进程异常连接。
- `file_download` 下载样本到控制端进一步分析。

### 4. Web / 数据库暴露面检查

适合新资产上线检查、内网服务盘点、应急期间快速摸清暴露面。

```bash
./deepsentry -c config.yaml --task "检查目标机开放端口，识别 Web、Redis、MySQL、PostgreSQL、Oracle 服务，判断是否存在弱配置或未授权访问风险。"
```

可用能力包括：

- `nmap_scan` / `cidr_scan` 做端口和网段探测。
- `service_fingerprint` 识别服务指纹。
- `http_probe` / `http_fetch` / `web_snapshot` 检查 Web 响应和页面。
- `redis_probe` / `mysql_probe` / `postgres_probe` / `oracle_probe` 做数据库连通性与基础风险探测。

### 5. 多台服务器批量巡检

适合多台靶机、业务集群、攻防演练环境、AWD 批量值守。

```text
对 prod 标签下的所有 SSH 目标执行系统巡检，检查 CPU、内存、磁盘、监听端口、最近登录、Web 目录变化和可疑进程，最后按主机汇总风险。
```

如果只需要执行低风险只读命令：

```text
对 selector=prod,ssh 的目标执行 uptime、df -h、ss -lntp，并汇总异常项。
```

Fleet 会根据 `selector` 匹配目标，并通过 `fleet_exec` / `fleet_file` 执行命令或文件操作。只读命令会尽量自动执行，写文件、删除、重启、上传等高风险动作会进入确认流程。

### 6. WebShell / 蚁剑场景后台执行

适合不能长时间保持交互的 WebShell、网页终端、受限终端。

```bash
./deepsentry --webshell -c config.yaml --task "后台排查当前机器的系统信息、Web 目录、可疑进程和最近登录，完成后生成报告。"
```

页面会立即返回报告路径和进度日志路径。你可以用：

```bash
cat reports/latest_webshell.txt
cat reports/webshell_progress_<timestamp>.log
cat reports/report_<timestamp>.md
```

### 7. 自动化定时巡检和通知

适合安全运营、值班巡检、比赛期间周期性检查。

```text
每天 9 点巡检生产服务器 CPU、内存、磁盘、监听端口和 SSH 登录异常，生成报告后发送到飞书和钉钉。
```

可配合 `schedule_task`、钉钉机器人、飞书机器人、HTTP 邮件网关，把本地报告同步给团队。网页设备每日巡检用 `kind=inspection`，见 [网页巡检与 Word 报告](#网页巡检与-word-报告)。

### 8. 用微信 / 飞书 / QQ 指挥 Agent

适合值班不在电脑旁、要在群里收报告、网页验证码需要人眼看一眼。

```bash
./deepsentry --chat-setup -c config.yaml
```

无桌面或 SSH/CMD：

```bash
./deepsentry --chat-setup-cli -c config.yaml
```

绑定后在私聊发送：

```text
巡检 https://设备地址 的运行状态和最近一天告警。网页用鹰眼，验证码问我；保留截图并发 Word 报告。
```

也可以说「重启会话」「切换会话」，或发 `/ds help` 看当前模型、工具和连接。完整指令见 [聊天机器人接入](#聊天机器人接入)。

### 9. 网页设备巡检并生成 Word

适合安全运营平台、防火墙/堡垒机 Web 控制台、需要截图留证的日常检查。

```text
用鹰眼打开目标登录页，登录后巡检总览、账号、日志和系统设置；保留真实截图，最后生成 Word 报告。
```

巡检结束后直接说「生成报告」。会从这次会话的结论和截图生成带封面的 Word，不必手写 JSON、不必安装 pandoc。设备清单模板见 [inspection.example.yaml](inspection.example.yaml)。鹰眼是 Hx0 产品，DeepSentry 已深度适配其 MCP。

### 10. 用 FofaMap 查互联网暴露面

FofaMap 同样是 Hx0 产品。接到 DeepSentry 后，用自然语言搜资产即可，不必手写 FOFA 语法。

```text
用 FofaMap 查授权范围内某产品的公网暴露面：先取官方规则，再搜索并汇总端口和标题，不要做主动扫描。
```

Agent 会走账户检查 → 产品规则 → 校验查询 → 搜索翻页。FOFA API Key 留在 FofaMap 本地，不要写进 DeepSentry 配置或 Git。主动漏洞扫描必须你明确授权。配置见 [FofaMap MCP 适配](docs/FofaMap-MCP-适配.md)。

---

## CTF / AWD / AWD-Plus 能力

DeepSentry 可以作为比赛和演练中的 AI 辅助队友。它不会替代人的判断，但能把大量重复检查、文件识别、服务巡检、证据汇总和多目标操作自动化。

### CTF 辅助

适合 Misc、Forensics、Web、Crypto 辅助分析、日志题、流量题、压缩包和文件杂项题的初筛。

| 需求 | 可以怎么用 |
| --- | --- |
| 找 flag | 使用 `flag_scan` 扫描目录、归档、文本和常见输出 |
| 判断未知文件 | 使用 `file_ident`、`file_strings`、`file_hash` 识别类型、字符串和哈希 |
| 分析压缩包 | 使用 `zip_password_recover action=auto` 按 ZipCracker 顺序处理伪加密、短明文 CRC32 和内置 6000 字典，再用 `archive_extract`、`read_gzip`、`archive_pack` 解压、查看和重新打包 |
| 看流量题 | 使用 `pcap_analyze` 提取会话、DNS、HTTP、可疑载荷和明文线索 |
| 看数据库题 | 使用 `sqlite_inspect`、`mysql_probe`、`redis_probe` 查看结构和数据线索 |
| 看 Web 题 | 使用 `http_probe`、`http_fetch`、`web_snapshot` 检查页面、响应头和可疑接口 |
| 写小脚本 | 使用 `script_run` 在授权环境中运行解码、统计、提取脚本 |

示例任务：

```text
分析当前目录下的题目附件，自动识别文件类型，尝试解压、查找 flag、提取可疑字符串，并把每一步证据写入报告。
```

```text
分析 capture.pcap，提取 HTTP 请求、DNS 查询、可疑明文、文件传输痕迹和可能的 flag。
```

```text
检查这个 Web 题目标站，识别响应头、页面源码、常见敏感路径和可疑参数，给出下一步测试方向。
```

### AWD 值守

适合多队互打、服务保活、批量检查、快速定位被打点机器。

| 需求 | 可以怎么用 |
| --- | --- |
| 服务可用性检查 | `awd_service_check`、`http_probe`、`service_fingerprint` |
| 批量查看状态 | `fleet_exec` 执行 `uptime`、`df -h`、`ss -lntp` 等只读命令 |
| Web 目录巡检 | `secret_scan`、`file_tail`、`read_log`、`file_hash` |
| 异常进程排查 | `process_list`、`net_connections`、`port_listen` |
| 快速取证 | `file_download`、`archive_pack`、报告输出 |
| 修复文件同步 | `fleet_file upload` 在确认后批量上传补丁或配置 |

示例任务：

```text
对所有 AWD 靶机检查 Web 服务是否存活，记录 HTTP 状态码、标题、响应时间和异常主机，最后按队伍/主机输出表格。
```

```text
检查所有靶机 /var/www/html 最近 30 分钟新增或修改的 PHP 文件，筛选可疑 WebShell 片段，并下载证据文件到本地 workspace。
```

```text
对所有靶机检查异常进程、反连连接、监听端口和计划任务，输出需要优先处理的机器列表。
```

### AWD-Plus 多目标协同

AWD-Plus 更强调多靶机、多服务、多阶段处置。DeepSentry 的 Fleet、子 Agent、定时任务和报告机制可以组合成持续值守流程。

| 场景 | 推荐组合 |
| --- | --- |
| 多靶机资产盘点 | `fleet_inventory` + `target_health_summary` + `service_fingerprint` |
| 多服务保活 | `awd_service_check` + `http_probe` + `schedule_task` |
| 分批并行分析 | 子 Agent + `target_selector`，每个子 Agent 负责一组目标 |
| 文件批量分发 | `fleet_file upload`，高风险确认后执行 |
| 漏洞修复后验证 | `fleet_exec` + `http_probe` + `web_snapshot` |
| 赛中报告复盘 | Markdown 报告 + checkpoint 会话恢复 |

示例任务：

```text
把 targets 中 tag=awd-plus 的机器按 Web、数据库、运维端口分组，分别检查服务存活、异常进程、敏感文件、WebShell 痕迹和登录异常，最后生成一份按优先级排序的处置清单。
```

```text
每 5 分钟检查 AWD-Plus 目标的 Web 服务状态、首页哈希、响应时间和最近错误日志。如果发现异常，把证据写入报告并发送飞书通知。
```

```text
对每台靶机分别派发子 Agent 审计今天的登录日志和 Web 访问日志，汇总攻击源 IP、受影响路径、可疑上传文件和建议封禁规则。
```

使用 CTF / AWD / AWD-Plus 功能时，请确保目标、靶机、比赛环境或演练环境均属于你拥有或明确授权的范围。

---

## 下载哪个文件

前往 [GitHub Releases 页面](https://github.com/asaotomo/DeepSentry/releases)，选择与 README 顶部版本号一致的 Release，再按自己的系统下载一个 `deepsentry-*` 主程序。如果页面尚未提供当前版本的预编译资产，请按下文从源码构建，不要把旧版二进制误认为当前版本。

CPU 架构简单判断：

- `amd64`：也叫 `x86_64` / `x64` / 64 位 x86。绝大多数 Intel / AMD 台式机、笔记本、云服务器都选这个。
- `386`：32 位 x86。只有非常老的 32 位系统才选；如果系统是 64 位，不要选 386。
- `arm64`：ARM 64 位。Apple Silicon Mac（M1/M2/M3/M4）、部分 ARM 服务器或树莓派 64 位系统选这个。

| 系统 | CPU | Release 文件名 | 运行方式 |
| --- | --- | --- | --- |
| macOS Apple Silicon | `arm64`，M1/M2/M3/M4 | `deepsentry-darwin-arm64` | `chmod +x deepsentry-darwin-arm64` |
| macOS Intel | `amd64`，Intel Mac | `deepsentry-darwin-amd64` | `chmod +x deepsentry-darwin-amd64` |
| Linux 64 位 x86 | `amd64` / `x86_64` / `x64` | `deepsentry-linux-amd64` | `chmod +x deepsentry-linux-amd64` |
| Linux ARM 64 位 | `arm64` / `aarch64` | `deepsentry-linux-arm64` | `chmod +x deepsentry-linux-arm64` |
| Linux 32 位 x86 | `386` / `i386` / `i686` | `deepsentry-linux-386` | `chmod +x deepsentry-linux-386` |
| Windows 64 位 x86 | `amd64` / `x64`，常见 Windows 电脑 | `deepsentry-windows-amd64.exe` | 双击或 PowerShell 运行 |
| Windows 32 位 x86 | `386` / `x86`，老 32 位系统 | `deepsentry-windows-386.exe` | 双击或 CMD 运行 |

建议把下载的主程序重命名为 `deepsentry`：

```bash
mv deepsentry-linux-amd64 deepsentry
chmod +x deepsentry
./deepsentry --version
```

macOS 如果提示“无法打开，因为无法验证开发者”，可以在终端执行：

```bash
xattr -d com.apple.quarantine ./deepsentry 2>/dev/null || true
chmod +x ./deepsentry
./deepsentry --version
```

Windows 推荐使用 Windows Terminal 或 PowerShell 7：

```powershell
.\deepsentry-windows-amd64.exe --version
```

如果 Release 同时提供 `SHA256SUMS`，建议在运行前校验文件完整性。

macOS / Linux：

```bash
shasum -a 256 -c SHA256SUMS
```

Windows PowerShell：

```powershell
Get-FileHash .\deepsentry-windows-amd64.exe -Algorithm SHA256
```

将 PowerShell 输出与 `SHA256SUMS` 中对应文件的哈希进行比较；不一致时不要运行该文件。

---

## 5 分钟快速开始

先完成本地一次只读任务，确认模型和终端都正常。聊天机器人和网页巡检是加分项，放在后面。

### 第 1 步：下载或编译二进制

如果你下载的是 Release：

```bash
chmod +x ./deepsentry
./deepsentry --version
```

如果你从源码构建：

```bash
git clone https://github.com/asaotomo/DeepSentry.git
cd DeepSentry
bash build.sh
./build/deepsentry --version
```

### 第 2 步：创建配置文件

推荐先运行交互式配置向导：

```bash
./deepsentry --init
```

如果你希望手动配置，也可以复制模板：

```bash
cp config.example.yaml config.yaml
```

如果你使用 `build/` 目录里的二进制：

```bash
cd build
./deepsentry --init
```

### 第 3 步：填入 AI 模型配置

初始化向导会让你选择模型/API 实际上下文长度：自动、64K、128K、256K、512K、1M、2M 或自定义。不确定时选“自动”；本地模型应以运行时真正加载的 `num_ctx` / `max_model_len` 为准。向导会把选择转换为精确的 `context_window_tokens`。

不使用向导时，打开 `config.yaml`，至少填写：

```yaml
provider: custom
api_protocol: auto
api_url: https://your-llm.example.com/v1
api_key: YOUR_API_KEY
model_name: your-model-name
agent_runtime: v3  # 默认；如需临时回滚可显式改为 legacy
```

初始化向导默认推荐 DeepSeek 最新视觉模型：

```yaml
provider: deepseek
api_protocol: auto
api_url: https://api.deepseek.com
api_key: YOUR_API_KEY
model_name: deepseek-flash
vision_mode: auto
agent_runtime: v3
```

`vision_mode: auto` 使用精确模型目录判断图片能力。当前内置的多模态默认模型包括 `deepseek-flash`、`deepseek-v4-flash-vision-exp`、`glm-5.3-flash`、`MiniMax-M3`、`mimo-v2.5`、`gpt-6-astra`（ChatGPT 6）、`gpt-5.6`、`claude-opus-5`、`claude-sonnet-5`、`gemini-3.8-flash` 和 `qwen3.8-max`；选择这些模型无需手动开启图片输入。相近的纯文本型号仍保持关闭，避免把 MCP 截图错误发给不支持图片的接口。

Runtime v3 默认启用结构化多工具调用、模型故障切换、可恢复执行断点和脱敏事件追踪。通常无需设置 `agent_runtime`；旧模型网关出现兼容问题时，可临时使用 `legacy` 模式排查。

### 第 4 步：选择目标模式

本地模式：

```yaml
target_protocol: local
ssh_host: ""
telnet_host: ""
ftp_host: ""
```

SSH 远程模式：

```yaml
target_protocol: ssh
ssh_host: "192.0.2.10:22"
ssh_user: root
ssh_password: "YOUR_PASSWORD"
ssh_key_path: ""
```

SSH 密钥模式：

```yaml
target_protocol: ssh
ssh_host: "192.0.2.10:22"
ssh_user: root
ssh_password: ""
ssh_key_path: "~/.ssh/id_ed25519"
```

### 第 5 步：运行第一个任务

TUI 模式，适合日常使用：

```bash
./deepsentry -c config.yaml
```

管道、cron、CI 里会自动改成普通终端输出；只有加 `--tui` 才强制全屏。

进入界面后输入：

```text
排查当前这台机器的系统版本、内存、磁盘、监听端口和最近登录，只读取证，最后给出风险结论。
```

脚本 / CI：

```bash
./deepsentry --no-tui -c config.yaml --task "查看当前系统版本和监听端口"
```

可选第 6 步：接聊天机器人（先确保上面这条任务能跑通）：

```bash
./deepsentry --chat-setup -c config.yaml
```

网页终端、蚁剑等非交互环境用 `--webshell`，见 [WebShell 用法](#webshell--蚁剑--非交互环境用法)，不要作为第一次体验。

---

## 聊天机器人接入

把 DeepSentry 接到微信、QQ、企业微信、飞书或钉钉后，值班不必守在终端前。这是可选项：先配好模型并在本机跑通一个任务，再绑定。

扫码只绑定**你自己的账号**。私聊可以直接下发任务；群聊必须加 `/ds` 前缀，并且要在配置里写明允许的群。完整字段和回调地址见 [聊天机器人接入](docs/聊天机器人接入.md)，没有图形界面时见 [终端接入与每日巡检](docs/终端接入与每日巡检.md)。

### 怎么打开绑定窗口

| 环境 | 命令 |
| --- | --- |
| 本机有浏览器 | TUI 输入 `/connect`，或 `./deepsentry --chat-setup -c config.yaml` |
| SSH / Linux 控制台 / Windows CMD | `./deepsentry --chat-setup-cli -c config.yaml`（字符二维码 + 官方链接） |
| 已绑定，只要常驻收消息 | `./deepsentry --chat -c /绝对路径/config.yaml` |
| 停止常驻 | `./deepsentry --chat-stop -c config.yaml` |

已扫码或已在 `chat.channels` 配好通道时，启动 DeepSentry 会自动拉起聊天进程；没有配置则不启动。聊天跟当前进程走：退出 TUI、`Ctrl+C` 结束 `--chat-setup`，对应子进程和任务一起停，不会再摘成独立后台。

### 支持的通道

| 平台 | 绑定方式 | 收消息 |
| --- | --- | --- |
| 飞书 | 官方 Device Flow 扫码 | WebSocket 长连接 |
| QQ | 官方绑定任务扫码 | QQ Bot Gateway |
| 微信 | iLink 二维码 | 长轮询 |
| 企业微信 | 官方 AI Bot 弹窗 + 一次性 `/ds pair` 配对 | WebSocket |
| 钉钉 | 企业内部应用机器人（HTTP 回调） | 消息回调 / sessionWebhook |
| OneBot / 微信桥接 | 手动配置入站密钥 | HTTP POST |

快速绑定默认只开放本人私聊。群聊必须在通道里显式写 `allowed_chats`。32 位构建不能跑飞书长连接 SDK，请用 64 位或改其他通道。

### 聊天里怎么用

```text
/ds help              查看模型、工具、MCP、连接（与启动面板一致）
/ds run 审计监听端口
/ds status
/ds result
/ds cancel
/ds new / 重启会话
切换会话              列出近期会话，回复序号恢复
继续                  预算暂停后沿用同一会话往下跑
```

私聊可直接发任务，不必加 `/ds`。同一私聊自动续聊；`/new` 或「重启会话」会取消旧执行与排队，旧确认作废。最多同时 5 路任务，同会话后续消息排队。

低风险自动执行。高危/中风险在**同一聊天会话**询问，回复「允许本次」「本会话同类」「本次会话允许所有高危操作」或「拒绝」。验证码先推截图再问你，下一条文字回到原任务，不会新开 Agent；「本会话所有高危」不会自动替你填验证码。

微信、QQ、飞书、企业微信、钉钉支持图片/文件/语音入站（以平台能力为准）；出站可回传图片和 Word。入站附件会在入队后立刻预取，避免短时签名 URL 过期。图片作为真实视觉内容交给模型，不是只传文件名。

---

## 网页巡检与 Word 报告

适合「设备有 Web 后台、要截图留证、最后交一份正式 Word」。你只要会说话，不必写 JSON，也不必安装 pandoc。

**鹰眼（HawkEye）** 是 Hx0 自家的浏览器自动化产品，不是第三方插件。DeepSentry 对它的 MCP 做了深度适配：登录、点击、截图、验证码辅助都会走鹰眼工具，而不是内置的简易浏览。没装它时仍可用固定采集或普通网页探测，但「登录后点进各菜单」会弱很多。说明见 [HawkEye MCP 深度适配](docs/HawkEye-MCP-深度适配.md)。

### 推荐说法

在全屏终端或机器人里直接说：

```text
巡检这台设备的运行状态和最近一天告警。网页用鹰眼打开，验证码问我；保留截图并生成 Word 报告。
```

也可以先报设备地址，或把 [inspection.example.yaml](inspection.example.yaml) 里的清单配进配置。流程是：打开真实页面 → 登录（验证码发到聊天，你回复后继续）→ 查看总览/账号/日志/设置并截图 → 说「生成报告」。

没有截图或结论时，报告里不会把检查项标成通过。

### Word 长什么样

- 整页封面：标题、目标地址、编制日期、巡检时段。
- 正文顺序：**一、总体结论 → 二、巡检详情 → 三、总结与建议**。
- 截图插在对应章节，不堆在文末。
- 同目录还有 Markdown、清单和原始截图，便于核验。

默认写到配置里的巡检输出目录（常见是 `reports/inspections/<时间>/report.docx`）。分享时请整目录一起给，并先脱敏。

### 固定采集与每日调度

不经过大模型、按清单采集时，使用同一份 [inspection.example.yaml](inspection.example.yaml) 里带 `command` / 选择器的设备段。

```bash
./deepsentry --inspect -c config.yaml
./deepsentry --inspect --inspect-selector tag:daily -c config.yaml
./deepsentry --scheduler -c /绝对路径/config.yaml
```

需要人眼看验证码的巡检从聊天触发；定时任务遇到登录验证码不会假装成功。不承诺绕过验证码或 MFA。验证码请在页面上由人完成，或走鹰眼的验证码辅助，不要让模型去执行页面里的读图脚本。

---

## 配置文件说明

完整示例见 [config.example.yaml](./config.example.yaml)。

### 最小可用配置

```yaml
provider: custom
api_protocol: auto
api_url: https://your-api.example.com/v1
api_key: YOUR_API_KEY
model_name: your-model-name

# 模型能力适配（推荐保持 auto）
model_profile: auto
model_parameter_b: 0
context_window_tokens: 0
context_utilization: 0
reserved_output_tokens: 0
native_tool_limit: 0

target_protocol: ssh
ssh_host: "192.0.2.10:22"
ssh_user: root
ssh_password: "YOUR_PASSWORD"
ssh_key_path: ""

use_native_tools: true
max_steps: 30
subagent_max_steps: 15
llm_timeout_sec: 120
llm_retries: 3
ssh_command_timeout_sec: 90
ssh_max_output_bytes: 524288

# 可选：TSecBench 跑分平台
benchmark_base_url: "https://tsecbench.zc.tencent.com"
benchmark_token: "YOUR_BENCHMARK_TOKEN"
```

### AI 服务商字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `provider` | 是 | 服务商名称，如 `mimo`、`openai`、`deepseek`、`qwen`、`custom` |
| `api_protocol` | 建议填 | `auto` 会自动识别常见协议 |
| `api_url` | 是 | API 地址，可只填到 `/v1` |
| `api_key` | 云模型必填 | API Key，请妥善保管；也可以用环境变量提供 |
| `model_name` | 建议填 | 模型名称，留空时使用 provider 预设 |
| `model_profile` | 否 | `auto` 根据本地/云端、参数量和窗口选择 `compact` / `balanced` / `full` |
| `model_parameter_b` | 否 | 本地模型参数量（B）；模型名含 `14b` / `70b` 时可自动识别 |
| `context_window_tokens` | 本地建议填 | 实际运行时窗口，而非模型卡理论上限；Ollama/LM Studio 应与 `num_ctx` / `max_model_len` 一致 |
| `context_utilization` | 否 | 可用窗口比例；0 按 profile 自动留出 provider 开销和输出空间 |
| `reserved_output_tokens` | 否 | 输出预留/上限；0 自动，不兼容 `max_tokens` 的网关会自动重试 |
| `native_tool_limit` | 否 | 每轮直接暴露的内置工具数；0 自动，未暴露工具仍可经 `tool_catalog` 发现 |
| `vision_mode` | 否 | `auto` 优先读取精确模型能力目录；自定义网关可显式设为 `enabled` 或 `disabled` |
| `llm_timeout_sec` | 否 | 单次 LLM 超时时间，建议 120 |
| `llm_retries` | 否 | LLM 重试次数，建议 3 |

TUI 标题栏会显示 DeepSentry 当前采用的有效上下文窗口，例如：

```text
deepseek / deepseek-flash · ctx≈1.00M[官方模型目录]
custom / qianfan-code-latest · ctx≈131.1K[安全默认]
```

- `=` 表示来自显式 `context_window_tokens` 配置；`≈` 表示系统根据模型名称、厂商预设或安全默认值推断。
- `[配置]` 是用户明确配置的运行窗口；`[名称推断]`、`[厂商预设]`、`[安全默认]` 都不是对服务商接口实时查询的结果。
- `ctx=1.05M[配置]` 表示 DeepSentry 会按 1,048,576 token 窗口管理上下文，不等于程序自动证明该模型服务确实支持 1M；配置值必须与服务端真实限制一致。
- 标题栏右侧的 `token 125.0K` 是当前会话的估算累计用量，不是模型上下文上限。

常用订阅套餐已内置为可选 provider，Base URL 会自动补全 `chat/completions`：

| provider | 套餐 | 预设 Base URL | 默认模型别名 |
| --- | --- | --- | --- |
| `qianfan` | 百度千帆 Coding Plan | `https://qianfan.baidubce.com/v2/coding` | `qianfan-code-latest` |
| `volcengine` | 火山方舟 Coding Plan | `https://ark.cn-beijing.volces.com/api/coding/v3` | `ark-code-latest` |
| `mimo` | Xiaomi MiMo Token Plan / MiMo Claw | `https://token-plan-cn.xiaomimimo.com/v1` | `mimo-v2.5` |

服务商的套餐名称、模型别名和接口地址可能调整；预设不可用时，请以服务商最新文档为准，并在 `config.yaml` 中显式覆盖 `api_url` 和 `model_name`。

常用国产模型的按量计费接口也已内置；向导保存后会自动补全 Chat Completions 路径：

| provider | Base URL | 默认模型 | 自动图片能力 | 上下文目录值 |
| --- | --- | --- | --- | --- |
| `deepseek` | `https://api.deepseek.com` | `deepseek-flash` | 是 | 1,000,000 tokens |
| `glm` | `https://open.bigmodel.cn/api/paas/v4` | `glm-5.3-flash` | 是 | 1,000,000 tokens |
| `minimax` | `https://api.minimax.cn/v1` | `MiniMax-M3` | 是 | 1,000,000 tokens |
| `mimo` | `https://token-plan-cn.xiaomimimo.com/v1` | `mimo-v2.5` | 是 | 1,000,000 tokens |
| `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen3.8-max` | 是 | 1,000,000 tokens |
| `hunyuan` | `https://tokenhub.tencentmaas.com/v1` | `hy4-preview` | 否 | 1,000,000 tokens |
| `openai` | `https://api.openai.com/v1` | `gpt-6-astra` | 是 | 1,050,000 tokens |
| `anthropic` | `https://api.anthropic.com/v1` | `claude-opus-5` | 是 | 1,000,000 tokens |
| `google` | `https://generativelanguage.googleapis.com/v1beta/openai` | `gemini-3.8-flash` | 是 | 1,048,576 tokens |
| `xai` | `https://api.x.ai/v1` | `grok-4.6` | 是 | 500,000 tokens |

Claude 走官方 Messages 接口（`https://api.anthropic.com/v1/messages`，`anthropic-version: 2023-06-01`）。`claude-opus-5` / `claude-sonnet-5` / `claude-fable-5-1` 默认开启 adaptive thinking，DeepSentry 会预留 64K `max_tokens` 并解析 `thinking` 块；`claude-haiku-4-5` 仍按普通输出额度。Gemini 继续用官方 OpenAI 兼容端点 `.../v1beta/openai`。千问默认已切到旗舰 `qwen3.8-max`。

### TSecBench 跑分配置

配置 `benchmark_base_url` 和 `benchmark_token` 后，Agent 会优先使用内置 `tsecbench` 工具完成平台流程：拉取题目、启动容器、访问靶场入口、提交 flag、关闭容器。也兼容平台下发的大写写法：

```yaml
BENCHMARK_BASE_URL: "https://tsecbench.zc.tencent.com"
BENCHMARK_TOKEN: "YOUR_BENCHMARK_TOKEN"
```

常用任务示例：

```bash
./deepsentry --no-tui -c config.yaml --task "跑 TSecBench，先列出题目并选择一道 easy 题启动容器，拿到 flag 后提交并关闭容器。不要输出 token 明文。"
```

TUI 快捷方式：

```text
/tsecbench
/tsecbench 先跑一题 easy，完成后提交并关闭容器
```

首次 `deepsentry --init` 时也会询问是否配置 `/tsecbench` 跑分模式；选择需要后输入 `benchmark_base_url` 和 `benchmark_token` 即可。

### Agent 步数控制

| 字段 | 默认值 | 说明 |
| --- | ---: | --- |
| `max_steps` | `30` | 主 Agent 初始步数窗口；关闭自适应时为固定上限 |
| `subagent_max_steps` | `15` | 子 Agent 初始步数窗口。开启 `execution_budget` 后可按新工具结果续步，受共享总预算约束 |

临时提高复杂任务的子 Agent 上限：

```bash
./deepsentry -c config.yaml --subagent-max-steps 30 --task "完整分析 auth.log 和 syslog，输出登录时间线、可疑 IP、提权行为和证据链"
```

复杂任务中，主 Agent 会优先把独立方向拆给子 Agent。例如日志、网络、Webshell 三个方向可以并行执行。运行器会去除完全重复的委派、限制总并发，并避免 target-aware 任务形成嵌套并发风暴。

每个子 Agent 都会收到主任务目标、当前 TODO 和会话核心线索。并行执行期间只共享有界的高信号线索板（IP、URL、CVE、哈希、路径、明确结论），不互相复制原始长对话；后续步骤可以读取其他子 Agent 刚发布的证据。线索保留来源，仍需结合证据区分用户提供、已验证事实和推断。完成后由主 Agent 按“已验证事实 / 证据 / 冲突与不确定项 / 下一步”合并结果。

长会话采用分层上下文：原始目标和最新用户修正固定保留，早期执行轨迹按预算摘要，最近步骤保留原文。摘要服务失败时仍会保留上一版有效摘要与核心线索。核心线索随 checkpoint 保存；真正需要跨会话长期使用的规则和偏好仍通过 `remember` 或 `AGENTS.md` 保存。

支持的 provider：

```text
openai, anthropic, google, deepseek, qwen, qianfan, volcengine, hunyuan, tencent_hy,
teleai, ctyun, minimax, mimo, glm, xai, grok, ollama, lmstudio, custom
```

### 目标连接字段

| 模式 | 关键字段 | 说明 |
| --- | --- | --- |
| 本地 | `target_protocol: local` | 所有命令在控制端本机执行 |
| SSH | `target_protocol: ssh`、`ssh_host`、`ssh_user` | 推荐模式，支持 Shell 和文件读写 |
| Telnet | `target_protocol: telnet`、`telnet_host` | 支持 Huawei VRP、H3C Comware、Ruijie RGOS、Cisco IOS 和 Linux Telnet；认证与命令 Prompt 分离、自动识别分页 |
| FTP/FTPS | `target_protocol: ftp`、`ftp_host` | 仅文件/目录能力；支持显式/隐式 TLS 和主动/被动数据通道，无 Shell |
| Fleet | `targets[]` | 多台目标批量运维 |

### 高级运行与安全配置

日常首次使用可以保持默认值；代理、浏览器、归档取证或严格 SSH 环境可按需配置：

| 字段 | 作用 |
| --- | --- |
| `controller_proxy` | 控制端统一出站代理，支持 `http://`、`https://`、`socks5://`、`socks5h://`；覆盖 LLM、MCP HTTP、HTTP/Web、TCP/CIDR/数据库探测及 SSH/Telnet/FTP |
| `browser_binary` | 指定 Chrome/Chromium 路径；留空时自动发现；找不到时工具会返回当前系统安装引导，静态网页能力仍可用 |
| `browser_timeout_sec` | 浏览器单次导航、快照或交互超时 |
| `browser_artifact_dir` | 网页截图等浏览器产物目录 |
| `archive_max_entries` | 本地安全解压允许的最大条目数 |
| `archive_max_file_bytes` | 单个解压文件的最大字节数 |
| `archive_max_total_bytes` | 单次归档解压后的最大总字节数，用于限制解压炸弹 |
| `ssh_host_key_policy` | SSH 主机密钥策略：`strict`、`accept-new` 或 `insecure`；生产环境推荐前两种 |
| `ssh_known_hosts_path` | 自定义 SSH `known_hosts` 文件路径 |
| `ssh_legacy_compat` | 默认 `true`。兼容老 OpenSSH / 交换机 / 防火墙的 `ssh-rsa`、DH-SHA1、CBC；仅现代算法时设 `false` |
| `ssh_connect_timeout_sec` | SSH 握手超时，默认 25 秒，老设备可加大 |
| `ssh_key_passphrase` | 加密私钥口令；未加密私钥可留空 |
| `telnet_prompt` | 老设备 Telnet 命令提示符；自动识别不稳定时显式配置 |

当 `known_hosts` 已固定某种主机密钥，而服务器同时提供新的 RSA、ECDSA 或 ED25519 密钥时，DeepSentry 会优先协商已固定的算法，避免把“同一服务器新增另一种密钥”误判为身份变化。如果已固定的密钥确实不再提供，程序会同时显示旧指纹和本次收到的新 SHA256 指纹；只有选择“核对并更新 SSH 主机密钥”、通过云控制台或管理员独立核对并再次确认后，才会替换该目标的旧记录。重新输入密码或启用 `ssh_legacy_compat` 都不会绕过主机身份校验。

启动时可像 fscan 一样临时指定代理，命令行优先于 `controller_proxy`，但不会写回配置文件：

```bash
./deepsentry -proxy http://127.0.0.1:8080 --task "扫描 192.168.1.0/24 的 80 端口并汇总 Web 服务"
./deepsentry -socks5 socks5://127.0.0.1:1080 --task "扫描 192.168.1.0/24 的常见端口"
```

`-proxy` 与 `-socks5` 互斥。HTTP/HTTPS 代理必须允许 `CONNECT` 才能承载 TCP 扫描和 SSH/Telnet/FTP；普通 HTTP 请求不受此限制。代理账号密码可写在 URL 中，例如 `http://user:pass@127.0.0.1:8080`，但不会显示在 Banner、模型上下文或报告中。显式代理影响控制端流量，目标机内部运行的命令不会经控制端代理。

### 浏览器级网页能力

已连接 HawkEye MCP 1.0.7 时，打开/播放真实网页（尤其 B 站、倍速、全屏）走 HawkEye，**不要**用内置 `browser_browse`。B 站播放任务先 `load_skill("bilibili-play")`：按 URL 导航、合集用 `?p=N`、倍速设 `video.playbackRate`、全屏 `press_key f`、黑屏截图改用 canvas 抓帧。大页面续读只传 `page_token`；验证码走 `browser_captcha_assist`。详见 [DeepSentry × Hx0 HawkEye MCP 深度适配](docs/HawkEye-MCP-深度适配.md)。

未连接 HawkEye 时，Agent 才优先调用 `browser_browse`，创建一个可持续复用的隔离浏览器会话：

- `mode=auto`：macOS/Windows/有桌面的 Linux 打开可见 Chrome 窗口；服务器环境自动使用无头模式。
- 页面快照会输出可见文字及 `@e1`、`@e2` 形式的交互元素引用，后续导航、前进、后退和截图继续复用同一个 `session_id`。
- 点击、输入、选择和按键由独立的高风险工具 `browser_interact` 执行，避免普通浏览误触提交或泄露输入内容。
- 浏览器使用临时隔离 profile，不读取个人 Chrome 的 Cookie、历史记录和已登录账号；会话关闭或 DeepSentry 正常退出时自动清理。
- 找不到 Chrome/Chromium 时，`action=status/open` 会按当前系统返回安装命令和 `browser_binary` 配置方式；简单页面仍可退回 `headless_browser`/`web_snapshot` 静态抓取。
- 仅当受控容器无法使用 Chromium sandbox 时，可显式设置 `DEEPSENTRY_BROWSER_NO_SANDBOX=1`；该模式会降低浏览器隔离强度，不得用于浏览不可信站点或处理真实凭据。

典型调用顺序：`browser_browse(open)` → `browser_browse(snapshot)` → 必要时 `browser_interact(click/type)` → `browser_browse(close)`。

完整默认值、通知通道、Fleet、Skills 和 MCP 示例见 [config.example.yaml](./config.example.yaml)。

### 让 Agent 管理 config.yaml

DeepSentry 内置 `config_manage` 工具，Agent 可以在用户明确要求时维护控制端本机的 `config.yaml`。所有写操作都会先在同目录创建备份：

```text
.deepsentry_backups/config_<timestamp>.yaml
```

支持的常见管理动作：

| 需求 | 工具动作 |
| --- | --- |
| 查看当前配置摘要 | `action=status`；兼容 `view/show/list/overview` |
| 读取指定配置项 | `action=get`，参数 `key` |
| 校验 YAML 是否可读 | `action=validate` |
| 手动创建备份 | `action=backup` |
| 添加外部 Skill 目录 | `action=add_skill_source`，参数 `source`，也兼容 `path/dir` 表示 Skill 目录 |
| 添加 MCP Server | `action=add_mcp_server`；stdio 使用 `name/command/args`，远程使用 `name/type/url`，可加工具白/黑名单与超时 |
| 导入 Claude Desktop MCP JSON | `action=import_claude_mcp`，参数 `import_path` 或 `content` |
| 启用/禁用 MCP Server | `action=enable_mcp_server` / `action=disable_mcp_server`，参数 `name` |
| 启用/禁用 Skill 来源 | `action=enable_skill_source` / `action=disable_skill_source`，参数 `source` |
| 按名称启用/禁用 Skill | `action=enable_skill` / `action=disable_skill`，参数 `name`；TUI 使用 `/skill on <name>` / `/skill off <name>`（`unload <name>` 也兼容） |
| 全局启用/禁用 Skill | `action=enable_skills` / `action=disable_skills`；TUI 使用无参数 `/skill on` / `/skill off` |
| 仅启用一个 Skill | TUI 使用 `/skill only <name>`；该命令会打开全局 Skill 开关、启用指定 Skill，并禁用其余已发现 Skill |
| 添加/更新 Fleet 目标 | `action=add_target`，参数 `protocol/host/user/password/key_path/tags` |
| 将已有单台配置转为 Fleet | `action=enable_fleet`，会把当前单台目标纳入 `targets` 并切到控制端模式 |
| 设置单台 SSH 目标 | `action=set_ssh`，参数 `host/user/password/key_path` |
| 修改允许的单值字段 | `action=set`，参数 `key/value` |
| 修复并替换整份配置 | `action=replace_yaml`，参数 `content` |

示例自然语言：

```text
把 /opt/deepsentry-skills 添加到 config.yaml 的 skill_sources，修改前先备份。
```

```text
把这台 SSH 机器添加为 Fleet 目标：host=10.0.0.8:22，user=root，password=YOUR_PASSWORD，tag=prod。
```

工具输出会隐藏密码、Token、Secret 等敏感值，但配置文件本身仍可能包含凭据，请保护好 `config.yaml` 和备份目录。

如果 `config.yaml` 已经损坏到无法解析，Agent 可以读取原文件、生成修复后的完整 YAML，再用 `replace_yaml` 校验并替换；替换前仍会保留旧文件备份。

### 不想把密钥写进 config.yaml？

可以用环境变量覆盖：

```bash
export DEEPSENTRY_API_KEY="你的 API Key"
export DEEPSENTRY_SSH_HOST="192.0.2.10:22"
export DEEPSENTRY_SSH_USER="root"
export DEEPSENTRY_SSH_PASSWORD="你的 SSH 密码"
./deepsentry -c config.yaml
```

建议：

- `config.yaml` 通常保存在运行 DeepSentry 的机器上，用来保存模型、目标和运行偏好。
- 如果不希望配置文件中出现密钥，可以使用环境变量注入 API Key、SSH 密码等敏感信息。
- 团队共享配置时，建议使用脱敏后的模板文件，并为不同环境准备不同的配置副本。

---

## 常用运行模式

### 1. 默认 TUI

```bash
./deepsentry -c config.yaml
```

适合人工值守排查、持续追问、多轮分析。

### 2. 直接带任务进入 TUI

```bash
./deepsentry -c config.yaml "排查目标机内存、磁盘和监听端口"
```

### 3. 经典 stdout 模式

```bash
./deepsentry --no-tui -c config.yaml --task "审计 SSH 登录失败记录"
```

适合普通终端、脚本、CI。

### 4. JSONL 自动化模式

```bash
./deepsentry --no-tui --json -c config.yaml --task "mem_info + port_listen" > events.jsonl
```

适合外部程序消费事件流。

### 5. 静默模式

```bash
./deepsentry --quiet -c config.yaml --task "查看当前系统状态"
```

### 6. 计划模式

```bash
./deepsentry --plan -c config.yaml --task "配置每天 9 点巡检 CPU、内存、磁盘并生成报告"
```

### 7. 无人值守模式

```bash
./deepsentry --batch -y -c config.yaml --task "自动巡检目标机 /proc"
```

`--batch -y` 会自动批准高风险动作，请只在受控环境使用。

### 8. 恢复会话

```bash
./deepsentry --list-sessions
./deepsentry --resume session_xxx -c config.yaml
```

TUI 图形化选择恢复：

```bash
./deepsentry --tui --pick-session -c config.yaml
```

### 9. 绑定聊天机器人

```bash
./deepsentry --chat-setup -c config.yaml
./deepsentry --chat-setup-cli -c config.yaml
./deepsentry --chat -c config.yaml
./deepsentry --chat-stop -c config.yaml
```

### 10. 网页巡检 / 固定采集

```bash
./deepsentry --inspect -c config.yaml
./deepsentry --inspect --inspect-selector tag:daily -c config.yaml
```

交互式 HawkEye 巡检更适合在 TUI 或聊天里用自然语言触发，结束后说「生成报告」。

---

## WebShell / 蚁剑 / 非交互环境用法

很多 WebShell 环境不适合 TUI，也不适合长时间阻塞等待。DeepSentry 提供 `--webshell` 专用模式。

### 基本命令

```bash
./deepsentry --webshell -c config.yaml --task "查看当前系统版本"
```

前台会立即返回类似：

```text
[WEB] DeepSentry 任务已提交后台执行
[WEB] 执行结果报告: /path/to/reports/report_YYYYMMDD_HHMMSS.md
[WEB] 实时进度日志: /path/to/reports/webshell_progress_YYYYMMDD_HHMMSS.log
[WEB] 任务状态文件: /path/to/reports/webshell_status_YYYYMMDD_HHMMSS.json
[WEB] 固定索引文件: /path/to/reports/latest_webshell.txt
[WEB] 查看进度: cat /path/to/reports/webshell_progress_YYYYMMDD_HHMMSS.log
[WEB] 查看状态: cat /path/to/reports/webshell_status_YYYYMMDD_HHMMSS.json
[WEB] 查看报告: cat /path/to/reports/report_YYYYMMDD_HHMMSS.md
```

注意这里推荐用 `cat`，不是 `tail -f`。很多 WebShell 对长连接和持续输出不友好，`cat` 更稳。

### 查看进度

```bash
cat reports/latest_webshell.txt
```

`latest_webshell.txt` 会列出最近一次任务的实际进度和报告路径，复制对应路径后再用 `cat` 查看。

### 查看报告

```bash
cat reports/report_YYYYMMDD_HHMMSS.md
```

请将示例中的 `YYYYMMDD_HHMMSS` 替换为程序返回的实际时间戳。

### WebShell 模式特点

- 父进程立即返回，不会卡住 WebShell 页面。
- 子进程后台执行，进度写入 `webshell_progress_<时间>.log`。
- 状态写入 `webshell_status_<时间>.json`，可直接检查 PID、退出码以及 queued/running/completed/failed；进度日志末尾也会写入完成标记。
- 最终报告写入 `report_<时间>.md`。
- `reports/latest_webshell.txt` 永远指向最近一次任务路径。
- 等同于后台启用 `--no-tui --batch -y`，高风险动作会自动批准。
- 如果 Agent 必须追问，任务会保存 checkpoint，可用 `--resume` 补充信息继续。

### WebShell 恢复任务

```bash
./deepsentry --webshell -c config.yaml --resume session_xxx --task "补充信息：目标 Web 目录是 /var/www/html"
```

---

## TUI 全屏界面用法

TUI 是默认模式：

```bash
./deepsentry -c config.yaml
```

常用快捷键：

| 快捷键 | 功能 |
| --- | --- |
| `Tab` | 聚焦输入框 |
| `Enter` | 发送任务或追问；若正在翻阅历史，提交后自动回到实时底部 |
| `Shift+Enter` / `Alt+Enter` / `Ctrl+J` | 输入换行 |
| `Ctrl+V` / `Ctrl+Shift+V` / macOS `⌘V` | 直接粘贴：图片优先生成附件卡，无图时回退到普通文本粘贴；macOS Command+V 仅在 DeepSentry 窗口前台时读取系统图片剪贴板 |
| `↑` / `↓` / `j` / `k` | 逐行翻阅活动日志 |
| `PgUp` / `PgDown` | 整页翻阅活动日志 |
| `Ctrl+Home` / `g` | 跳到当前保留记录的顶部 |
| `Ctrl+End` / `G` | 跳到底部并恢复自动跟随 |
| `Esc` | 中断当前任务或退出输入状态 |
| `Ctrl+A` | 全选输入内容；再按删除即可清空 |
| `Ctrl+L` | 清屏 |
| `Ctrl+U` | 清空输入 |
| `S` | 允许本次会话所有高危操作（`/new` 或切换会话后失效） |
| `e` | 全部展开折叠项；再次按下全部折叠 |
| `Y` | 仅批准当前这一次操作 |
| `A` | 本会话内允许确认面板标明的同类范围；普通参数可变化，但目标、操作类型和敏感凭据边界不变 |
| `N` | 拒绝当前风险确认面板中的操作 |
| `q` | 空闲时退出 |

长文本粘贴（超过 2 行或 800 字符）会显示为紧凑的“粘贴文本”块，完整内容仍会发送给 Agent；短粘贴直接显示原文。粘贴后输入的补充文字保持可见、可编辑。多行或自动换行输入中，`↑` / `↓` 优先移动光标，到达边界后才切换历史。

在输入框直接按 `⌘V`（macOS）或 `Ctrl+V` 即可粘贴：剪贴板含图片时立即生成附件卡，没有图片时自动粘贴文本。macOS 上终端会先处理 Command+V 的文本粘贴；仅当 DeepSentry 所在窗口处于前台时，才会把图片剪贴板贴进附件卡。也可使用 `/image /绝对或相对路径/screen.png`；不带路径的 `/image` 会读取系统图片剪贴板。支持 PNG/JPEG/GIF/WebP，单张最多 20 MiB、单条消息最多 8 张且合计最多 40 MiB。图片草稿以紧凑卡片显示，纯图片也可直接发送。`vision_mode: auto` 优先识别精确的官方模型 ID，目前会为 `deepseek-flash`、`gpt-6-astra`、`glm-5.3-flash`、`MiniMax-M3`、`mimo-v2.5`、`claude-opus-5`、`qwen3.8-max` 自动开启图片输入；自定义视觉模型名无法识别时设为 `enabled`。文本 fallback 不会接收图片。

`A` 会话授权按“同一目标 + 同一工具/操作类型”复用，ID、正文和 payload 等普通参数可以变化；文件路径、Shell 命令、主机/服务端等目标身份和密码、Token 等敏感参数变化仍会重新询问。授权仅在当前运行中的会话控制器内有效，新建或恢复 checkpoint 时清空。

斜杠命令：

| 命令 | 说明 |
| --- | --- |
| `/help` | 查看帮助 |
| `/connect` | 打开「连接通讯工具」窗口，绑定微信 / QQ / 飞书等 |
| `/new` | 新建任务 |
| `/restart` | 重新开始 |
| `/clear` | 清屏 |
| `/status` | 查看状态 |
| `/cost` | 查看 token 使用量 |
| `/model` | 查看当前模型 |
| `/image [路径]` | 附加图片；省略路径读取系统图片剪贴板，可连续附加后输入问题并发送 |
| `/compact` | 压缩长上下文提示 |
| `/memory list` | 查看跨会话结构化 Memory |
| `/memory clues [clear]` | 查看或清空当前会话核心线索板 |
| `/memory clear [all\|target\|global]` | 按范围清理持久化 Memory |
| `/agents status\|clear` | 查看 AGENTS.md 来源，或清空外部 AGENTS.md（内置默认保留） |
| `/sessions` | 查看可恢复会话 |
| `/resume <session_id> [补充说明]` | 在当前 TUI 中恢复并继续 checkpoint；会重建真实提问和结论轨迹 |
| `/tsecbench [任务说明]` | 进入 TSecBench 跑分模式，可直接附加题目或目标说明 |
| `/config` | 查看配置摘要 |
| `/sudo` | 由系统 `sudo -v` 安全验证/刷新本机管理员授权；密码不进入 DeepSentry |
| `/mcp status\|reconnect\|import\|add\|login\|resources\|read\|prompts\|prompt\|off\|on\|remove` | 管理 stdio / Streamable HTTP MCP Server，并调试断线重连、Resources 与 Prompts |
| `/skill find\|inspect\|install\|managed\|updates\|update\|pin\|unpin\|uninstall\|rollback\|audit` | 跨 ClawHub / skills.sh 管理市场 Skill |
| `/skill list\|rescan\|load\|unload\|add\|off\|on\|only\|source-off\|source-on\|remove` | 管理 Skill、全局/单项开关和本地来源目录 |
| `/exit` / `/quit` | 退出 |

输入 `/` 会显示命令联想，输入 `/c` 可快速补全 `/clear`。

询问面板和最终报告都会解析 Markdown。表格在空间足够时显示为对齐网格，窗口较窄时自动改成逐条键值布局；Emoji 和组合字符按完整字素计算宽度。模型需要补充信息或要求用户选择时，TUI 会进入询问面板并等待输入；模型服务偶尔返回非标准格式时，程序会尝试恢复询问、Shell 代码块或最终结论，无法可靠恢复时会要求模型重试。

本机命令需要 `sudo` 且尚未授权时，TUI 会暂停全屏并把终端交给系统 `sudo -v`。密码由系统隐藏读取，不经过 DeepSentry 输入框，也不会写入会话、报告、Memory 或发给模型；验证成功、失败或取消后，TUI 会重新进入备用屏幕、恢复鼠标跟踪并完整重绘，滚轮仍用于翻阅会话，不会穿透到系统终端历史。验证后实际命令统一使用 `sudo -n`，避免再次抢占 TUI stdin。也可在空闲时先输入 `/sudo`。Batch、WebShell 和其他非交互模式绝不会弹密码框，缺少授权时立即失败。远程 SSH/Telnet 只允许 `sudo -n` 或最小范围 `NOPASSWD`，不会假设 SSH 密码等于 sudo 密码。

---

## 内置工具清单

当前版本注册 73 个内置工具。它们由 Go 原生实现或统一调度，Agent 会按需发现和调用，不会每轮把全部工具塞进 prompt。

### 按场景分类

| 场景 | 工具 |
| --- | --- |
| 网络连通 | `ping`、`traceroute`、`dns_lookup`、`bandwidth_test` |
| 连接审计 | `net_connections`、`port_listen`、`route_table`、`arp_table`、`firewall_status` |
| 网络设备 | `network_device_baseline`、`network_device_diagnose` |
| 系统应急 | `mem_info`、`process_list`、`target_health_summary`、`disk_usage`、`file_tail`、`login_audit`、`service_units`、`file_hash` |
| 确定性工作流 | `host_incident_baseline`、`webshell_hunt`、`competition_answer_check` |
| 取证分析 | `file_ident`、`file_strings`、`read_gzip`、`read_log`、`pcap_analyze`、`sqlite_inspect`、`zip_password_recover` |
| 文档解析 | `document_parse` |
| 聊天文件 | `chat_send_file` |
| 端口和内网 | `nmap_scan`、`cidr_scan`、`netcat_probe`、`service_fingerprint` |
| HTTP / Web | `http_probe`、`http_fetch`、`web_snapshot`、`headless_browser`、`browser_browse`、`browser_interact` |
| 抓包和流量 | `flow_snapshot`、`packet_capture` |
| 进程与连接关联 | `proc_socket_map` |
| 数据库探测 | `redis_probe`、`mysql_probe`、`postgres_probe`、`oracle_probe` |
| 配置审计 | `app_config_discover`、`db_config_audit`、`db_log_read`、`secret_scan`、`service_unit_audit`、`container_inventory` |
| CTF / AWD / 跑分 | `flag_scan`、`awd_service_check`、`tsecbench` |
| 脚本和文件 | `script_run`、`file_download`、`file_upload`、`archive_pack`、`archive_extract` |
| 代理转发 | `tcp_forward`、`socks5_proxy` |
| 自动化任务 | `schedule_task`、`inspection_run` |
| 扩展与配置 | `config_manage`、`skill_market`、`mcp_resource`、`mcp_prompt` |
| Fleet 批量 | `fleet_inventory`、`fleet_exec`、`fleet_file` |

### 工具风险等级

| 风险 | 含义 |
| --- | --- |
| low | 只读或低影响操作 |
| medium | 会主动连接目标、读取较敏感信息或产生明显探测行为 |
| high | 可能执行脚本、上传文件、扫描端口、抓包、代理转发或批量执行 |

交互 TUI 模式下，经过对应风险策略后仍被最终判定为高风险的动作才会请求确认。`--batch` 在用户确认进入无人值守模式后会自动批准，`--batch -y` 和 `--webshell` 会跳过人工确认，请只在受控环境使用。

确认面板支持 `Y` 仅本次、`A` 本会话允许同类范围、`N` / `Esc` 拒绝；`Enter` 仍默认拒绝。会话授权不是全局开关：文件修改按“动作类型 + 精确路径”匹配，Shell 命令按“目标 + 完整命令”匹配；带 `action/operation/mode` 的工具按“目标 + 工具 + 操作类型”匹配，已注册 MCP 工具可把工具名本身作为操作类型，普通 ID、正文和 payload 变化不再重复询问。任意代码执行、Shell/Terminal、文件写入/上传类 MCP 工具仍按完整参数匹配；目标身份或密码、Token 等敏感参数变化也会重新询问。新建、重启或恢复会话后授权自动清空，不写入 checkpoint。

Shell 与 Fleet 使用以下动态判险逻辑：

- 直接 Shell 命令使用双层判定：规则只读直接放行；规则判高后由 AI 复核，只有两层都判高才人工确认，AI 复核不可用时失败关闭。`2>&1` 等描述符合并不再当作写文件。
- `fleet_exec` 等高风险工具仍按工具契约和真实 `command` / `cmd` 内容判定；这类工具的确认边界不由命令 AI 复核代替。
- `fleet_file` 的 `ls`、`read`、`download` 可自动执行；`upload` 会写入目标文件，需要确认。

### 工具启用/禁用

默认全部启用。可以在配置中控制：

```yaml
enabled_tools: []
disabled_tools:
  - tcp_forward
  - socks5_proxy
  - file_upload
  - script_run
```

如果 `enabled_tools` 非空，它会作为白名单：

```yaml
enabled_tools:
  - mem_info
  - port_listen
  - net_connections
  - read_log
  - secret_scan
```

---

## 多目标 Fleet 用法

Fleet 适合一次管理多台服务器、网络设备或 FTP 证据机。

### 配置示例

```yaml
targets:
  - name: web-01
    protocol: ssh
    host: "10.0.0.11:22"
    user: root
    password: ""
    key_path: "~/.ssh/id_ed25519"
    tags: ["prod", "web"]

  - name: legacy-router
    protocol: telnet
    host: "10.0.0.2:23"
    user: admin
    password: YOUR_PASSWORD
    device_type: huawei # auto | huawei | h3c | ruijie | cisco | linux | generic
    auth_prompt_regex: "" # 非常规 Passcode 提示才需要
    prompt: "<Core-Router>" # 仅命令阶段；留空自动捕获
    tags: ["legacy", "network"]

  - name: ftp-backup
    protocol: ftp
    host: "10.0.0.50:21"
    user: backup
    password: "YOUR_PASSWORD"
    tags: ["backup", "evidence"]
```

Telnet 网络设备不会使用 Linux Shell marker。运行时会等待真实 CLI prompt，并自动处理 Huawei/H3C 的 `screen-length 0 temporary`、Ruijie/Cisco 的 `terminal length 0`、ASA `terminal pager 0`、Juniper `set cli screen-length 0` 及常见 `More` 分页。配置 `enable_password` 后，Huawei/H3C 登录会自动执行 `super`，Ruijie/Cisco/ASA/山石/深信服会自动执行 `enable`；密码会脱敏且不会出现在命令输出中。连接后优先调用 `network_device_baseline` 采集版本、板卡、接口、路由、STP 和日志。

Huawei/H3C 的 `system-view` 是配置态，不等同于 `super`，因此不会在连接时自动进入。经过高风险审批后可以显式执行 `system-view`，运行时会跟踪 `<设备名>`、`[设备名]` 及子视图 prompt 的变化；`quit` / `return` 退出视图属于低风险导航。保存配置、端口启停以及 ACL、路由、VLAN 修改仍需风险审批。

SSH 目标也支持同样的网络设备 CLI。默认开启旧协议兼容：老服务器的 `ssh-rsa` 主机密钥、DH-SHA1、AES-CBC/3DES，以及设备常用的 keyboard-interactive 登录都可协商。`ssh_device_type: auto` 会在常见 Linux/SFTP 不可用时自动回退到 PTY 交互会话（`vt100`/`xterm` 回退）；比赛或生产巡检建议显式填写 `huawei` / `h3c` / `ruijie` / `cisco` / `asa` / `juniper` / `fortinet` / `paloalto` / `hillstone` / `sangfor` / `checkpoint`，避免协议探测浪费时间。

FTP 目标只提供目录和文件能力，不执行 Shell。`ftp_tls_mode` 支持 `plain`、`explicit`（AUTH TLS）和 `implicit`（默认 990）；FTPS 默认校验系统信任链和主机名，私有 CA 用 `ftp_tls_ca_file` 指定，不建议开启 `ftp_tls_insecure_skip_verify`。TLS 后会强制 `PBSZ 0` + `PROT P`，控制命令与文件数据都受保护。

`ftp_data_mode: passive` 优先 EPSV（兼容 IPv6/NAT）并回退 PASV；PASV 固定使用控制连接对端 IP，避免 FTP bounce/SSRF。`active` 优先 EPRT 并在 IPv4 老设备上回退 PORT，只接受控制连接对端的回连；NAT 环境可用 `ftp_active_address` 指定对外 IP。主动模式需要入站防火墙放行临时端口，不能经 `controller_proxy`。`auto` 先尝试被动通道，再回退主动通道。

控制、连接、数据传输可分别用 `ftp_command_timeout_sec`、`ftp_connect_timeout_sec`、`ftp_transfer_timeout_sec` 调整。下载先写 0600 临时文件，收到成功终态后再原子替换目标。传统 `plain` FTP 仍为明文，只应在受控隔离网使用；生产环境优先 FTPS 或 SSH/SFTP。

VRP/Comware 的 `| include` / `exclude` 是区分大小写的“匹配行投影”，不等于传输截断。DeepSentry 会在这类结果后标记 `projection=filtered, output_truncated=false`；只有超过输出上限时才标记 `output_truncated=true`。单接口排查建议直接执行完整 `display interface <interface>`，不要用长串 include 丢失上下文。

比赛可使用 `deepsentry --competition --task "<题目>"`。该模式会优先限时快诊、证据绑定、修复后复验和 AI 纠错，最终报告按任务状态、结论、证据、处置、复验、AI 复核、风险/回滚的顺序生成。

### selector 规则

| selector | 命中 |
| --- | --- |
| `all` | 全部目标 |
| `web-01` | 按 name |
| `10.0.0.11` | 按 host |
| `ssh` / `telnet` / `ftp` | 按协议 |
| `prod` / `web` | 按 tag |
| `prod,ssh` | 同时匹配多个条件 |

### 常见任务

列出目标：

```text
列出当前 Fleet 目标清单
```

批量巡检：

```text
对 prod 标签的 SSH 主机执行内存、磁盘、监听端口巡检，最后汇总异常。
```

批量命令：

```text
对 selector=prod,ssh 的目标执行 uptime 和 df -h，并汇总结果。
```

### 连接和密码注意事项

如果目标已经写在 `targets[]` 或单台 SSH 配置里，不要让 Agent 在控制端手写裸 `ssh/scp/sftp root@host ...`。这些系统命令不会读取 DeepSentry 的 `config.yaml` 密码/私钥，可能直接卡在 OpenSSH 的交互式密码提示里。

正确方式：

```text
对 target-01 执行 echo SSH_OK
```

Agent 应调用：

```json
{"action":"tool","tool_name":"fleet_exec","tool_args":{"selector":"target-01","command":"echo SSH_OK","concurrency":"1"}}
```

下载文件应走 `fleet_file`：

```json
{"action":"tool","tool_name":"fleet_file","tool_args":{"selector":"target-01","action":"download","remote_path":"/tmp/flag.txt","local_path":"~/.deepsentry/workspace/flag.txt"}}
```

需要每台机器独立分析时，优先使用子 Agent 的 `target_selector`，例如让 `log-analyst` 分别分析 `prod` 目标并汇总。

---

## 长上下文与多 Agent 协作

### 分层长上下文

DeepSentry 会自动整理长会话，不需要用户反复复制前情：

- 固定保留第一条真实用户目标和最新补充/修正；
- 固定保留上一版成功摘要和会话核心线索；
- 按模型的实际 token 窗口和预留输出动态决定何时压缩，1M 模型不再被固定 60K 字符阈值提前截断；
- 早期命令、输出、文件变化、失败原因和 TODO 按 token 分块、分层摘要，巨大单条日志也不会只留首尾；
- 近期原文数量随 profile 调整：`compact` 8 条、`balanced` 12 条、`full` 24 条，但 token 预算始终优先；
- 摘要失败或 API 报上下文超限时，机械保留目标、最新修正、上次摘要、核心线索和最近步骤后自动重试一次；
- `AGENTS.md`、Memory、Skills、MCP 说明和直接 Native Tool schema 都按 profile 分配预算，小模型优先获得短指令和任务相关工具。

本地模型若未声明窗口，系统会保守按 32K 运行并在启动时提示。例如：

```yaml
# 14B/20B/30B 通常保持 auto，会选 compact
provider: ollama
model_name: qwen2.5-14b-instruct
model_parameter_b: 14
context_window_tokens: 32768   # 必须与 Ollama 实际 num_ctx 一致

# 70B 本地模型会选 balanced；长窗口仍以服务端实值为准
model_name: llama-70b
model_parameter_b: 70
context_window_tokens: 131072
```

### 会话核心线索板

运行过程中会自动汇聚最多 48 条高信号候选事实，包括 IP、URL、CVE、哈希、Flag、文件路径和明确结论。同一线索由多个子 Agent 或多台目标发现时会合并来源；密码、Token、私钥等敏感值不会写入线索板。

```text
/memory clues        # 查看当前会话线索和来源
/memory clues clear  # 仅清空当前会话线索板
```

核心线索会随 checkpoint 保存和恢复，但不会自动升级为永久 Memory。需要跨会话保存的事实仍使用 `remember`，长期规则使用 `AGENTS.md`。

### 并发子 Agent

主 Agent 委派时会向每个子 Agent 提供：主目标、用户最新修正、当前 TODO、已有核心线索和唯一分工。子 Agent 使用独立历史和输出目录，只通过有界线索板共享高信号证据，不互相复制完整对话。

| 任务类型 | 最大并发 |
| --- | ---: |
| 本地独立子任务 | 4 |
| 带 `target_selector` 的并行任务 | 3 |
| 并行任务内部的目标展开 | 1 |

调度器会去除完全相同的任务、在停止后取消运行任务并阻止排队任务启动。`parallel_tasks` 内含 `target_selector` 时也会进入多目标风险确认流程。

推荐按独立证据方向拆分：

```json
{
  "action": "task",
  "parallel_tasks": [
    {
      "task_name": "log-analyst",
      "task_prompt": "只分析今天 auth.log，输出异常 IP、时间线和原始证据；完成后停止"
    },
    {
      "task_name": "network-analyst",
      "task_prompt": "只分析 established 连接和 DNS，输出远端、PID 和证据；完成后停止"
    },
    {
      "task_name": "webshell-hunter",
      "task_prompt": "只检查 Web 根目录近期修改文件，输出路径、哈希和代码证据；不得修改文件"
    }
  ]
}
```

并行结束后，主 Agent 会收到任务成功/失败数量、耗时、新增核心线索与来源，并按“已验证事实、证据、冲突/不确定项、下一步”合并结果。有依赖关系的任务应分两批执行：先并行取证，再围绕第一批线索定向复核。

更完整的触发规则、失败降级、两阶段协作和排障方法见 [操作手册：长上下文、核心线索与并发协作](docs/操作手册.md#53-长上下文核心线索与并发协作)。

---

## 报告、会话与记忆

### 报告位置

每次任务都会生成 Markdown 报告：

```text
reports/report_<timestamp>.md
```

网页巡检再说「生成报告」时，会额外写出正式 Word，一般在：

```text
reports/inspections/<时间>/report.docx
reports/inspections/<时间>/report.md
```

若你把输出目录配在 `build/reports` 下，路径会相应变化。会话 Markdown 含任务、动作和结论；巡检 Word 按「总体结论 → 巡检详情 → 总结与建议」排版并嵌入截图。聊天绑定密钥不在报告目录里，也不要把密钥文件提交到 Git。

### 会话恢复

DeepSentry 会保存 checkpoint：

```text
~/.deepsentry/sessions/<session_id>/checkpoint.json
```

查看会话：

```bash
./deepsentry --list-sessions
```

恢复会话：

```bash
./deepsentry --resume session_xxx -c config.yaml
```

### 记忆层

| 记忆 | 位置 | 说明 |
| --- | --- | --- |
| 内置 AGENTS.md | 二进制内置 | 默认行为准则 |
| 用户 AGENTS.md | `~/.deepsentry/AGENTS.md` | 用户级偏好 |
| 项目 AGENTS.md | `.deepsentry/AGENTS.md` | 项目级偏好 |
| KV 记忆 | `~/.deepsentry/memory/store.json` | Agent 保存的结构化事实 |
| 会话核心线索 | checkpoint 中的 `state.core_clues` | 当前会话最多 48 条高信号线索及来源，不自动跨会话推广 |

明显的密码、Token、私钥、Webhook 密钥会被记忆系统拒绝保存。

---

## 外部 MCP 与 Skills 扩展

DeepSentry 支持加载一部分 Claude / Codex / OpenClaw / Hermes 生态中常见的外部能力，但不是所有格式都能无缝通用。

### 外部 Skills

DeepSentry 的 Skill 加载规则很简单：一个目录就是一个 Skill，目录里必须有 `SKILL.md`。

默认加载目录：

```text
./skills
~/.deepsentry/skills
```

推荐目录结构：

```text
~/.deepsentry/skills/
└── log-audit/
    └── SKILL.md
```

`SKILL.md` 示例：

```markdown
---
name: log-audit
description: Linux 登录日志审计与异常来源分析
license: Apache-2.0
---

# Log Audit

当用户需要分析 auth.log、secure、syslog 登录异常时，按以下流程执行……
```

如果你的外部 Skill 本身就是 `SKILL.md` + YAML frontmatter 结构，通常可以直接复制到 `~/.deepsentry/skills/<skill-name>/SKILL.md` 使用。加载器兼容 Claude 的 `disable-model-invocation` / `user-invocable`，以及 Codex `agents/openai.yaml` 中的 `policy.allow_implicit_invocation`；目录元数据按需披露并有 8,000 字符预算。

DeepSentry 也可直接导入本地 `.skill` 文件。由于开放 Agent Skills 规范定义的是“包含 `SKILL.md` 的目录”，并没有统一 `.skill` 容器，DeepSentry 同时兼容两种常见形式：ZIP 包（内含唯一 `SKILL.md` 及 scripts/references/assets），以及直接将单文件 `SKILL.md` 保存为 `.skill`。导入会复用市场安装的路径逃逸、符号链接、数量/体积限制、静态风险扫描、SHA-256 来源锁和原子落盘，不会执行包内脚本。

内置 `find-skills` 会调用原生 `skill_market`，同时搜索 ClawHub 与 skills.sh。搜索阶段不会执行 `npx`、`clawhub` 或第三方脚本；安装前会检查市场安全状态、YAML、路径逃逸、符号链接、文件数量/体积和危险指令模式，并记录来源、版本与 SHA-256 锁。市场标记可疑或本地静态审查有警告时，安装会先停下，必须在人工复核后显式使用 `acknowledge-risk`。

也可以在 `config.yaml` 指定额外来源目录：

```yaml
skill_sources:
  - "skills"
  - "~/.deepsentry/skills"
  - "/opt/deepsentry-skills"
disabled_skill_sources:
  - "/opt/old-skills"
```

TUI 快捷命令：

```text
/skill list
/skill find log forensics
/skill inspect clawhub:security-audit
/skill install skills:owner/repo@skill-name
/skill inspect "/path/My Skills/log-audit.skill"
/skill import "/path/My Skills/log-audit.skill"
/skill updates
/skill update skill-name
/skill pin skill-name
/skill rollback skill-name
/skill uninstall skill-name
/skill load log-audit
/skill unload log-audit
/skill only fofamap
/skill add /opt/deepsentry-skills
/skill source-off /opt/old-skills
/skill source-on /opt/old-skills
/skill remove /opt/old-skills
```

例如系统发现 `fofamap`、`fun-brainstorming` 和 `log-audit`，只保留 `fofamap` 时直接输入：

```text
/skill only fofamap
```

不需要先执行 `/skill on`。该命令会自动打开全局 Skill 开关、立即应用到当前会话并写入配置，同时禁用其余当前已发现的 Skill。执行 `/skill list` 可以核对最终状态。

覆盖更新会保留可恢复备份；`pin` 会让批量更新跳过当前版本，`rollback` 可按版本或摘要前缀恢复，`uninstall` 默认移动到受控备份而非永久删除。`load` 只加载到当前会话；`unload/off/on/only` 会持久化 Skill 启停策略；Skill 来源目录请使用 `add/source-off/source-on/remove`。

### 外部 MCP

DeepSentry 使用官方 Tier-1 MCP Go SDK，支持 stdio 与 Streamable HTTP。任意兼容的 MCP 服务都可以接。**Hx0 鹰眼** 和 **Hx0 FofaMap** 是我们自己的产品：DeepSentry 按它们的正式工具表做了点对点适配（契约、超时、风险分级、任务路由），不是只把 JSON 转发给模型。

旧 stdio 短格式仍兼容：

```yaml
mcp_servers:
  - "fs:npx:-y,@modelcontextprotocol/server-filesystem,/tmp"
```

含义是：

```text
名称:启动命令:参数1,参数2,参数3
```

Agent 会协商 MCP 协议并分页读取 Tools、Resources、Resource Templates 与 Prompts；`list_changed` 会原子热刷新能力。MCP Tools 使用 Server 提供的 JSON Schema 作为一等原生函数暴露；紧凑 profile 会把有限 schema 预算分给与当前任务最相关及已经调用过的 MCP 工具，其余工具仍可通过 `agent_action` 按需调用。工具使用 `<server>__<tool>` 规范名，只有不冲突时才提供短别名，避免多个 Server 同名工具互相覆盖。

HawkEye MCP 会启用 1.0.7 全部 51 工具的本地能力契约，而不只是通用 MCP 转发：紧凑模型会按当前意图选中抓包、拦截、Research、真实手势、视觉、播放、验证码或审计工具链；默认超时按工具提升到 100/130/205 秒。HawkEye 已连通时覆盖内置 `browser_browse`。B 站播放先加载 `bilibili-play`：倍速用 `video.playbackRate`，全屏用 `press_key f`，合集用 `?p=N`，全屏黑屏用 canvas 抓帧。常规浏览、标签页新建/选中、点击输入、trusted input、播放校验 JS、抓包启停以及拦截启停不再打断 Agent 弹确认；仅 Scope 变更、Replay/Fuzz、请求放行/丢弃、上传和任意 JS 等真正高风险动作保留强确认。需 `userActivation` 时明确使用 `clickMode/inputMode=trusted`，大页面把 `next_page_token` 作为 `page_token` 单独续读（1.0.7 拒绝数字 `cursor`）；拦截任务必须 `disable` 收尾，连接关闭时还有最后的防挂起清理。HawkEye 或其他 MCP 返回的图片会保存到 `reports/mcp-artifacts/`（可用 `DEEPSENTRY_MCP_ARTIFACT_DIR` 覆盖），校验哈希后作为下一轮视觉输入。安装、工具矩阵、工作流和 Firefox 真实输入排障见 [DeepSentry × Hx0 HawkEye MCP 深度适配](docs/HawkEye-MCP-深度适配.md)。

FofaMap MCP 会启用 v2.0.1 全部 15 个工具的本地契约：资产查询先检查账户/字段，产品规则使用 `fofa_rules` 返回的 `app=`，查询先 `fofa_validate_query` 再 `fofa_search`，翻页原样传递 `next_cursor`（也接受该字段作为 `cursor` 别名）。`fofa_export`/`nuclei_plan` 的嵌套 `request` 可由扁平 `query`/`targets` 自动包装。只有用户明确授权主动扫描时才走 `nuclei_plan → 人工确认 → nuclei_execute`；查询类低风险、导出/计划中风险、执行扫描高风险。连接后先 `load_skill("fofamap")`。安装、密钥边界、stdio/HTTP 配置和完整工具矩阵见 [DeepSentry × FofaMap MCP 适配](docs/FofaMap-MCP-适配.md)。

推荐使用结构化格式。stdio 示例：

```yaml
mcp_server_configs:
  - name: fs
    type: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    cwd: "/tmp"
    env:
      EXAMPLE_TOKEN: "xxx"
    disabled: false
```

Streamable HTTP 示例：

```yaml
mcp_server_configs:
  - name: docs
    type: streamable_http
    url: https://mcp.example.com/mcp
    bearer_token_env_var: MCP_DOCS_TOKEN
    headers:
      X-Workspace: security
    enabled_tools: [search, read]
    disabled_tools: [delete]
    startup_timeout_sec: 30
    tool_timeout_sec: 120
    required: false
```

远程地址必须使用 HTTPS，只有 localhost / 回环地址允许 HTTP。Bearer Token 推荐放环境变量；需要 OAuth 时执行 `/mcp login <name>`，浏览器授权令牌只保留在当前进程，不写入配置文件。

TUI 快捷命令：

```text
/mcp status
/mcp import ~/Library/Application Support/Claude/claude_desktop_config.json
/mcp add fs npx -y,@modelcontextprotocol/server-filesystem,/tmp
/mcp add docs https://mcp.example.com/mcp token_env=MCP_DOCS_TOKEN enabled_tools=search,read
/mcp login docs
/mcp reconnect docs
/mcp resources docs
/mcp read docs docs://guide
/mcp prompts docs
/mcp prompt docs review topic=authentication
/mcp off fs
/mcp on fs
/mcp remove fs
```

当前支持：

| 能力 | 状态 |
| --- | --- |
| stdio / Streamable HTTP | 支持；远程强制 HTTPS（回环地址除外） |
| MCP tools/list 与 tools/call | 支持；分页、结构化内容、超时、工具白/黑名单、`list_changed` 热刷新 |
| Claude Desktop JSON 配置直接导入 | 支持，使用 `/mcp import <json路径>` 或 `config_manage action=import_claude_mcp` |
| env / cwd 细粒度启动参数 | 支持，使用 `mcp_server_configs` |
| MCP resources / templates / prompts | 支持；Agent 工具 `mcp_resource` / `mcp_prompt` 与 `/mcp` 调试命令均可访问 |
| MCP server instructions | 支持；按连接注入并限制长度 |
| Bearer / 自定义 Headers / OAuth | 支持；OAuth 需用户主动 `/mcp login`，不持久化明文 Token |
| 断线恢复 | `/mcp status` 显示重连提示，`/mcp reconnect <name>` 用原配置重建会话并重新发现能力；不会自动重放结果不确定的工具调用 |
| 多 Server 同名工具 | 使用规范名消歧，短别名只在唯一时开放 |
| 旧式 HTTP+SSE transport | 不新增支持；使用当前 MCP Streamable HTTP |

---

## 定时任务与多通道通知

DeepSentry 的定时任务可以在生成本地报告后发送外部通知。当前支持：

| 通道 | notify 值 | 配置 |
| --- | --- | --- |
| 钉钉机器人 | `dingtalk` | `dingtalk_webhook`，可选 `dingtalk_secret` |
| 飞书/Lark 机器人 | `feishu` | `feishu_webhook`，可选 `feishu_secret` |
| HTTP 邮件网关 | `email` | `email_gateway_url`、`email_to`，可选 token/header/from |

`notify` 支持逗号多选，例如 `dingtalk,feishu,email`，会按顺序同时发送。

基础配置：

```yaml
scheduler_enabled: true
scheduler_store: reports/schedules/tasks.json
scheduler_interval_sec: 30
scheduler_timezone: Local

# 钉钉机器人
dingtalk_webhook: ""
dingtalk_secret: ""

# 飞书/Lark 自定义机器人
feishu_webhook: ""
feishu_secret: ""

# HTTP 邮件网关
email_gateway_url: ""
email_gateway_token: ""
email_gateway_header: Authorization
email_to: "secops@example.com"
email_from: "deepsentry@example.com"
```

创建钉钉任务：

```text
每天 9 点巡检服务器 CPU、内存、磁盘和监听端口，生成报告并发钉钉。
```

创建飞书任务：

```text
每天 9 点巡检服务器 CPU、内存、磁盘和监听端口，生成报告并发飞书。
```

创建多通道任务：

```text
每天 9 点巡检生产服务器，生成报告，同时发钉钉、飞书和邮件通知。
```

也可以显式调用工具：

```text
使用 schedule_task 添加任务：每天9点巡检，notify=dingtalk,feishu,email，kind=inspection。
```

定时任务是持久化操作，当前采用保守意图门控：

- 只有“提醒我”、“帮我”、“安排”、“定时”、“创建任务”或以重复/相对时间开头的直接指令才进入快速创建。
- 只出现“明天”、“几点”、“执行”等词不会落盘；安全题答案、日志、HTTP 记录和代码块会被排除。
- `action=plan` 只预览；`action=add/create` 必须显式带 `confirm_create=true`。泛化 Agent 无人值守还需 `allow_batch=true` 和 `confirm_unattended=true`。
- 相同任务、执行时间、时区和重复规则的重复提交是幂等的，不会再写入一份。

邮件网关请求格式为 HTTP JSON POST：

```json
{
  "to": ["secops@example.com"],
  "from": "deepsentry@example.com",
  "subject": "DeepSentry 定时任务: 巡检",
  "markdown": "# 报告正文",
  "text": "报告正文",
  "source": "DeepSentry"
}
```

鉴权规则：

- `email_gateway_header: Authorization` 时，`email_gateway_token` 会以 `Bearer <token>` 发送。
- 如果你的网关使用 API Key，可设置 `email_gateway_header: X-API-Key`。
- 如果需要自定义完整 header，可写成 `email_gateway_header: "X-Token: {token}"`。

只运行调度器：

```bash
./deepsentry --scheduler -c config.yaml
```

查看、添加、删除、立即运行调度任务，也可以让 Agent 调用 `schedule_task` 工具完成。

## 从源码构建

### 环境要求

- Go 1.26.8 或更高版本。
- macOS、Linux 或 Windows。
- 如需远程模式，需要目标 SSH/Telnet/FTP 可达。

### 拉取代码

```bash
git clone https://github.com/asaotomo/DeepSentry.git
cd DeepSentry
```

中国大陆网络可配置 Go 代理：

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

### 构建当前平台

这种方式适合只需要当前系统二进制的用户。它不会注入 `build.sh` 中的构建日期参数，所以 `--version` 看到的 build 日期可能是代码默认值；如果需要生成 README 和 Release 中列出的全平台文件，请使用下一节的 `bash build.sh`。

当前平台（macOS / Linux / Windows）：

```bash
go build -trimpath -buildvcs=false -o deepsentry ./cmd
```

Windows PowerShell 中可将输出名改为 `deepsentry.exe`。必须构建整个 `./cmd` 包，不要手工枚举 `.go` 文件；Go 会按当前系统自动选择平台实现。

### 一键生成全平台二进制

```bash
bash build.sh
```

`build.sh` 会生成全平台二进制和 `build/SHA256SUMS`。发布前建议先运行 `go test ./...`，再执行构建和产物校验：

```bash
bash build.sh
(cd build && shasum -a 256 -c SHA256SUMS)
```

输出目录：

```text
build/
  deepsentry-darwin-amd64
  deepsentry-darwin-arm64
  deepsentry-linux-amd64
  deepsentry-linux-arm64
  deepsentry-linux-386
  deepsentry-windows-amd64.exe
  deepsentry-windows-386.exe
  deepsentry
```

---

## 常见问题

### 1. 提示找不到配置文件

运行：

```bash
./deepsentry --init
```

或显式指定：

```bash
./deepsentry -c config.yaml
```

### 2. SSH 连接失败

检查：

- `ssh_host` 是否包含端口，例如 `192.0.2.10:22`（请替换为你已授权的实际目标）。
- `ssh_user` 是否正确。
- 密码或 `ssh_key_path` 是否正确。
- 在系统终端里单独测试 `ssh root@<已授权目标> -p 22` 是否可达。注意不要让 DeepSentry Agent 通过裸 `ssh/scp/sftp` 访问已配置目标；应使用 `fleet_exec` / `fleet_file`。
- 云服务器安全组是否放行。

### 3. 已经在 config.yaml 写了密码，为什么还提示 `root@host's password:`？

通常是 Agent 生成了控制端裸 `ssh/scp/sftp` 命令。OpenSSH 子进程不会读取 DeepSentry 的配置文件，也不会把密码提示交给 TUI 输入框。

解决办法：

- 退出当前卡住的进程，重新启动当前构建的二进制。
- 把多目标访问改成 `fleet_exec` / `fleet_file`。
- 单台远程模式下直接执行目标命令，不要再包一层 `ssh root@host`。

### 4. WebShell 没有持续输出

WebShell 模式不会在前台持续刷屏，而是写入进度文件：

```bash
cat reports/latest_webshell.txt
cat reports/webshell_progress_<timestamp>.log
cat reports/report_<timestamp>.md
```

### 5. WebShell 报告没有生成

优先看进度日志：

```bash
cat reports/webshell_progress_<timestamp>.log
```

常见原因：

- `config.yaml` 路径不对。
- API Key 无效。
- SSH 连接失败。
- 目标命令超时。

### 6. LLM 报 429 / rate limit

说明模型服务商限流或高负载。可以：

- 稍后重试。
- 保持默认 `llm_retries: 3`；已使用退避和随机抖动，不建议在持续 429 时盲目增大。
- 增大 `llm_timeout_sec`。
- 更换模型或服务商。

当供应商级重试全部耗尽时，DeepSentry 会保存 checkpoint 并停止当前轮，避免外层 Agent 继续放大限流。服务恢复后使用 `--resume <session_id>` 继续。

### 7. 终端乱码或显示错位

尝试：

```bash
DEEPSENTRY_PLAIN=1 ./deepsentry -c config.yaml
DEEPSENTRY_ASCII=1 ./deepsentry --no-tui -c config.yaml --task "查看系统状态"
./deepsentry --no-color -c config.yaml
./deepsentry --theme light -c config.yaml
```

TUI 默认使用 `terminal_theme: auto`，会探测终端背景明暗并自动切换高对比深色/日间色板。无法响应背景查询的远程终端可在 `config.yaml` 固定 `terminal_theme: dark` 或 `terminal_theme: light`，也可用 `--theme auto|dark|light` 临时覆盖。Markdown 表格与正文会自动分隔紧贴文字的 Emoji，不修改原始命令和证据。

Windows 推荐 Windows Terminal 或 PowerShell 7。

如果只有 Emoji、Markdown 表格或询问框右边线错位，请先确认终端字体支持对应字符，并确认运行的是当前构建的二进制。确实不支持 Emoji 的终端可用 `DEEPSENTRY_PLAIN=1` 稳定降级。

如果模型偶尔不返回标准结构，程序会尝试恢复普通 Markdown；残缺响应会要求模型重试。若仍连续出现空响应，再检查模型兼容性、API 网关是否截断正文，以及 `llm_timeout_sec`。

如果出现 `root@host's password:`，通常是控制端启动了裸 `ssh/scp/sftp`。先按 `Ctrl+C` 中断，再让 Agent 使用 `fleet_exec` / `fleet_file` 访问已配置目标；不要把目标密码输入 TUI，也不要把密码拼进命令。

本机命令需要管理员权限时，DeepSentry 会暂时把终端交给系统 `sudo -v`。此时只在系统密码提示中输入，密码不会进入 TUI；完成、取消或验证失败后会恢复全屏界面。也可以在 TUI 空闲时执行 `/sudo`，或在启动程序前运行 `sudo -v`。不要使用 `echo 密码 | sudo -S`，也不要把 sudo 密码写进任务或配置。

### 8. TUI 退出后终端状态不正常

运行：

```bash
reset
stty sane
```

### 9. 不想让 Agent 执行高风险工具

在 `config.yaml` 禁用：

```yaml
disabled_tools:
  - script_run
  - file_upload
  - archive_extract
  - tcp_forward
  - socks5_proxy
  - nmap_scan
  - cidr_scan
  - packet_capture
```

### 10. 绑定了机器人，为什么启动后收不到消息？

- 确认用的是当前构建的 `./deepsentry` 或 `./build/deepsentry`，先发 `/ds help` 做真实联调。
- 已配置通道时，启动 TUI 会自动拉起聊天；退出 TUI 聊天一并停止。要单独常驻请用 `--chat`。
- 扫码成功不等于消息权限已通；微信可能还要短信验证码，飞书需订阅 `im.message.receive_v1`。
- 无桌面请用 `--chat-setup-cli`，不要转发绑定窗口的本机链接。

### 11. 巡检完说「生成报告」没有 Word？

- 先确认这次会话里已经有结论和截图，再说「生成报告」。不必安装 pandoc。
- 看配置里的巡检输出目录，常见是 `reports/inspections/<时间>/report.docx`。
- 没有真实截图或结论时，不会把检查项标成通过。
- 聊天绑定用的密钥不会出现在报告目录里，也不要提交到 Git。

---

## 安全建议

- 只在授权环境使用。
- 请妥善保管包含 API Key、SSH 密码、私钥路径、Webhook 的配置文件和备份文件。
- 分享截图、报告、压缩包或配置模板前，请先脱敏主机名、IP、账号、Token、密码和业务路径。
- 生产环境慎用 `--batch -y`。
- SSH 正式环境请使用 `ssh_host_key_policy: accept-new` 或 `strict`，不要使用 `insecure`。
- Telnet 和 `ftp_tls_mode: plain` 会明文传输凭据与数据，仅限受控隔离网；生产优先 SSH/SFTP 或开启严格证书校验的 FTPS。
- 远程安全解压采用“下载到控制端 → 安全解压与核验 → 再上传”；直接在远程目标解压会被失败关闭。
- WebShell 模式会自动批准动作，建议只在隔离环境或应急授权场景使用。
- 高风险工具如 `script_run`、`file_upload`、`tcp_forward`、`socks5_proxy` 默认存在确认机制；无人值守时请先配置 `disabled_tools`。
- 已配置目标请使用 `fleet_exec` / `fleet_file` / `target_selector`，不要在 Agent 内裸跑 `ssh/scp/sftp` 访问目标。
- 生成报告可能包含敏感路径、主机名、日志片段，公开前请脱敏。
- 聊天绑定只授权本人私聊；群聊必须显式配置白名单。不要把绑定窗口链接转发到公网。
- 巡检截图应框选状态/告警区域，避免把口令、验证码答案或无关个人信息写进 Word。

---

## 项目结构

```text
cmd/                    CLI 入口、TUI/经典/WebShell 参数处理
internal/analyzer/      LLM 协议、JSON/action 解析
internal/harness/       Agent 循环、动作执行、中间件、checkpoint
internal/tui/           全屏 TUI 界面
internal/chat/          微信 / QQ / 企业微信 / 飞书 / 钉钉聊天通道
internal/inspection/    网页巡检采集与 Word / Markdown 报告
internal/builtin/       Go 原生内置工具实现
internal/tools/         工具注册表与调度
internal/executor/      Local / SSH / Telnet / FTP / Fleet 执行器
internal/memory/        内置 AGENTS.md、用户记忆、KV 记忆
internal/scheduler/     本地定时任务
internal/security/      命令风险评估
docs/操作手册.md          详细中文操作手册
docs/聊天机器人接入.md    通讯工具绑定与指令
docs/终端接入与每日巡检.md  无桌面绑定与巡检流程
hawkeye-mcp-server.mjs   随附的 Hx0 鹰眼 MCP Server 1.0.7
inspection.example.yaml  巡检设备清单（聊天 + 可选固定采集）
config.example.yaml      配置模板
build.sh                 一键交叉编译脚本
```

---

## License

本项目采用 Apache License 2.0 开源协议，详见 [LICENSE](./LICENSE)。

---

## Credits

DeepSentry v2.0.4 Ultimate is developed by [Hx0 Studio](https://hx0studio.com/).

同一工作室的配套产品：

- [鹰眼 HawkEye](https://hx0studio.com/)：浏览器自动化，DeepSentry 已深度适配其 MCP 1.0.7
- [FofaMap](https://hx0studio.com/)：FOFA 资产测绘客户端，DeepSentry 已深度适配其 MCP v2.0.1

Author: asaotomo
