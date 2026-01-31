<div align="center">

# 🛡️ DeepSentry

<h3>"让 AI 成为你的红蓝对抗伙伴与运维专家。"</h3>

<p>
    <i>Your AI-powered Security Agent for Local & Remote Auditing.</i>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Team-Hx0-red?style=flat-square" alt="Team">
  <img src="https://img.shields.io/badge/Platform-Windows%20%7C%20Linux%20%7C%20macOS-gray?style=flat-square&logo=linux&logoColor=white" alt="Platform">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/AI-DeepSeek%20V3-blueviolet?style=flat-square" alt="AI">
</p>

[功能特性](#features) • [实战案例](#cases) • [快速开始](#quick-start) • [架构解析](#structure)

</div>



<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769763739593-6a9ccd4d-faa3-47bd-8b2d-b265a78137e3.png)

---

## 📖 项目简介
**DeepSentry** 是由 **Hx0 Team** 研发的一款智能化安全排查与运维 Agent。

它不仅仅是一个自动化脚本，更是一个**具备逻辑推理能力的“数字队友”**。

DeepSentry 内置了基于大语言模型（LLM）的决策大脑，能够理解复杂的自然语言指令，自动拆解安全任务，并在**本地环境**或**远程主机**上执行精准的排查与响应。

**它解决了传统安全工具的痛点：**

- 🚫 **告别死记硬背**：无需记忆复杂的 Linux/Windows 命令行参数，直接说人话。
- 🤝 **打破环境孤岛**：无论你是在 **Mac** 上分析本地文件，还是在 **Windows** 上管理 Linux 服务器，亦或是进行**本地与远程的混合编排**（如：从远程下载样本并在本地进行分析），DeepSentry 都能完美胜任。
- 🧠 **专家级决策**：内置动态风控引擎，自动识别高危操作，让 AI 的每一次执行都安全可控。

## <span id="features">✨ 核心特性</span>
+ **🧠 智能决策大脑**：集成 DeepSeek/OpenAI 等模型，支持自然语言交互。AI 会根据你的意图自动编写 Shell/Python 脚本并执行。
+ **🌐 跨平台原生支持**：完美运行于 Windows (自动处理 GBK 乱码)、macOS 和 Linux。
+ **🎯 混合执行引擎**：支持 **本地模式 (Local)** 直连和 **远程模式 (SSH)** 无感切换。
+ **🌉 本地/远程协同 (Bridge)**：AI 可自主决定何时使用 `upload` 上传工具到服务器，或使用 `download` 将日志/样本取回本地进行深层分析。
+ **🛡️ 动态风控系统**：内置命令风险评估引擎。`ls`、`ps` 等低风险命令自动执行；`rm`、`kill` 等高危命令需人工确认。
+ **📝 全自动审计报告**：自动生成 Markdown 格式的审计报告，包含 AI 的思考路径 (Thought)、执行的命令 (Command) 及最终结论，支持时间戳回溯。

---

## <span id="cases">⚡️ 实战案例 (Real-world Scenarios)</span>
以下案例均来自 **DeepSentry 真实运行日志**，展示了 AI 如何处理复杂的安全需求。

### 场景一：日志取证与威胁情报关联
**需求**：在海量日志中找出攻击源，并关联地理位置信息。

> **User**: "检查系统的 /var/log/auth.log，帮我找出过去 24 小时内所有登录失败超过 5 次的 IP 地址TOP 10，并统计归属地。"
>

**🤖 AI 思考与执行**:

1. **逻辑拆解**: 检查文件存在性 -> `grep` 筛选时间与失败字段 -> `awk/sort` 统计 Top 10 -> `curl` 调用 IP 库查询归属地。
2. **执行结果 (自动生成)**:

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769764344214-2b656e83-ef15-4c81-b455-5835309d90d4.png)

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769764357369-1e0e6491-1ac3-48c5-8a85-c3a0b7d1cbc5.png)



> **💡 价值**: AI 自动完成了一整套应急响应流程，发现攻击源高度集中在罗马尼亚，为封禁策略提供了直接依据。
>

### 场景二：系统脆弱性扫描与加固
**需求**：快速排查系统层面的配置缺陷。

> **User**: "扫描当前系统是否存在空口令用户，检查 UID 为 0 的账户，并查看防火墙是否开放了高危端口（如 445, 3389）。"
>

**🤖 AI 思考与执行**:

1. **多维排查**: 检查 `/etc/shadow` 空字段 -> 检查 `/etc/passwd` UID -> 检测 `ufw/iptables` 状态 -> 使用 `ss` 扫描监听端口。
2. **执行结果 (自动生成)**:

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769764428142-be555317-d533-4ec5-93a1-cacc5119370d.png)

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769764442941-ba0acc48-9102-4b12-ba80-3ca088c759ec.png)

> **💡 价值**: **一句话替代繁琐的基线核查脚本**。AI 不仅能机械地列出信息，还能根据安全常识自动判断“风险等级”，并关联账户与网络状态，直接给出运维视角的整改建议。
>

### 场景三：CTF 辅助与跨平台协同 (Bridge & Solve)
**需求**：将本地文件上传至靶机并自动解题。

> **User**: "帮我把本机的flag.txt上传到服务器的/tmp目录下并进行解出这道题目"
>

**🤖 AI 思考与执行**:

1. **环境感知**: 识别本机为 macOS，使用 `local_run ls` 确认文件。
2. **上传协同**: 使用 `upload` 将文件传输至 Linux 靶机。
3. **智能解密**: 读取文件内容 -> 识别为 Base64 编码 -> 自动解码。

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769763827932-9d31bb45-e1a8-468f-8268-1cf0116ea397.png)

> **💡 价值**: 展示了 DeepSentry 强大的**跨平台上下文感知能力**（Mac端与Linux端命令自动切换）以及**自动化解题**能力。
>



### 场景四：Webshell 隐蔽后门狩猎 (Web Hunt)
**需求**：在 Web 目录中寻找可能被攻击者植入的混淆后门，并进行自动化处置。

**User**: "在 /var/www/html 目录帮我分析里面有没有可疑的webshell文件。"

**🤖** **AI 思考与执行**:

1. **初步尝试**: 使用 `find` 结合 `grep` 搜索 `eval`, `base64_decode` 等危险函数。

- *AI 发现*: 扫描结果包含大量误报（如 `jquery.uploadify` 和 CMS 核心文件）。

1. **策略调整 (Pivot)**: AI 分析认为基于签名的搜索噪声太大，决定转向**行为分析**——查找最近 30 天内被修改过的 PHP 文件。

- *Command*: `find ... -mtime -30 ...`

1. **锁定目标**: 发现可疑文件 `/var/www/html/protected/apps/member/hacker.php`。
2. **取证分析**: 读取文件内容进行确认。

- *Result*: `<?php @eval($_POST['hacker']); ?>` (确认为一句话木马)。

1. **自动处置**: 在 Batch 模式下，AI 决定立即删除威胁。

- *Command*: `rm .../hacker.php`

**智能研判**:

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769766641666-4775f82d-d61b-4b04-8c34-42d18bcf5348.png)

> **💡 价值**: 超越传统的正则匹配查杀。AI 能理解代码逻辑，有效识别变形木马，是应急响应中快速止损的利器。
>

---

## <span id="quick-start">🚀 快速开始 (Quick Start)</span>
### 1. 下载与安装
确保您的机器已安装 [Go 1.20+](https://golang.org/dl/) 环境。

```bash
# 克隆项目
git clone https://github.com/asaotomo/DeepSentry.git

cd DeepSentry

# ⚠️ [中国大陆用户必选] 配置 Go 代理，防止依赖下载失败
go env -w GOPROXY=[https://goproxy.cn](https://goproxy.cn),direct

# 整理并下载依赖 (这一步显式下载所有包)
go mod tidy

# 编译 (支持 Mac/Linux/Windows)
go build -o deepsentry cmd/main.go

# (可选) Windows 用户如果是 CMD 环境，建议编译为 exe
# go build -o deepsentry.exe cmd/main.go
```

### 2. 初始化配置 (Wizard Mode)
首次运行，DeepSentry 会启动交互式向导，引导您配置 API 和连接信息：

```bash
./deepsentry -init
```

+ **AI 配置**: 支持 DeepSeek (推荐)、OpenAI、或本地 Ollama。
+ **SSH 配置**: 可选。若不配置则默认在**本地模式**运行。

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769763991987-d053e009-9fbf-44ec-8317-31da4b51bf58.png)

### 3. 开始使用
直接运行并输入需求，或通过参数指定：

```bash
# 查看帮助
./deepsentry -h
```

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769782063161-162277f6-6c3f-4d63-bfc2-1518a2b8e6e5.png)

```bash
#交互模式
./deepsentry
```

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769782429943-1f367dd8-f89b-4ee4-90e5-25d2811a4713.png)

```bash
#命令行参数模式
./deepsentry 帮我查询服务器的版本信息
```

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769782462399-13299bce-4b7e-45c2-b4fb-3ae292c76793.png)

```bash
# 无人值守模式，AI生成的任何命令不受限制，可能有风险 (Batch)
./deepsentry -batch "帮我整理一下/tmp目录下的文件，并归类到对应文件夹"
```

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769764237510-7d1bf312-d072-4161-a717-7cc4908123a1.png)

<!-- 这是一张图片，ocr 内容为： -->
![](https://cdn.nlark.com/yuque/0/2026/png/12839102/1769764274333-6144863f-416a-4ae5-bc7d-caa4e0e74a29.png)

---

## <span id="structure">📂 目录结构</span>
```latex
DeepSentry/
├── cmd/                 # 入口文件 (Windows/Unix 兼容处理)
├── config.yaml          # 配置文件 (API Key, SSH Host)
├── internal/
│   ├── analyzer/        # [大脑] LLM 交互核心与 Prompt 构造
│   ├── collector/       # [感知] 自动识别 OS (Ubuntu/CentOS/Win/Mac)
│   ├── executor/        # [手脚] 封装 SSH 与 Local 执行器，支持 Upload/Download
│   ├── security/        # [卫士] 风险命令拦截 (CheckRisk)
│   ├── skills/          # [技能] 预置 Go 原生安全函数
│   └── logger/          # [记忆] 生成 Markdown 审计报告
└── reports/             # [产出] 存放每一次任务的完整报告

```

---

## ⚠️ 安全说明
1. **风险控制**: 默认开启风险拦截。高危操作（如删除文件、停止服务）需要您输入 `y` 确认。
2. **自我保护**: AI 被禁止删除自身的配置文件及报告目录。
3. **敏感数据**: 您的 API Key 仅保存在本地 `config.yaml` 中，不会上传至任何第三方服务器。

---

## 🤝 贡献与反馈
DeepSentry 处于快速迭代中，欢迎提交 Pull Request 增加更多 `skills` 或适配更多模型！

+ **Bug 反馈**: 请在 Issues 页面提交。
+ **功能建议**: 欢迎在 Discussions 中讨论。

---

**DeepSentry** is proudly crafted by **Hx0 Team** 🇨🇳 _Code with passion, secure with intelligence._

