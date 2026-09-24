---
name: webshell-hunt
description: Webshell 隐蔽后门狩猎 — 在 Web 目录中识别混淆后门、一句话木马、最近修改的可疑文件
license: Apache-2.0
---

# Webshell 狩猎 Skill

## 何时使用

- 用户要求在 Web 目录中查找后门/木马
- 应急响应中需要快速定位 Webshell
- CMS/框架目录出现异常文件

## 工作流程

### 阶段 1: 签名扫描（快速但误报多）

```bash
find /var/www -name "*.php" -exec grep -l "eval\s*(" {} \; 2>/dev/null
find /var/www -name "*.php" -exec grep -l "base64_decode" {} \; 2>/dev/null
find /var/www -name "*.jsp" -exec grep -l "Runtime.getRuntime" {} \; 2>/dev/null
```

### 阶段 2: 行为分析（降低误报）

```bash
# 最近 30 天修改的 PHP 文件
find /var/www -name "*.php" -mtime -30 -ls 2>/dev/null

# 非框架目录的可疑文件
find /var/www -name "*.php" -not -path "*/vendor/*" -not -path "*/node_modules/*" -mtime -7
```

### 阶段 3: 内容确认

对可疑文件使用 `read_file` 或 `cat` 查看内容，识别：
- `<?php @eval($_POST[...]); ?>` — 一句话木马
- `base64_decode` + `eval` 组合 — 混淆后门
- `system()` / `exec()` / `shell_exec()` — 命令执行

### 阶段 4: 处置建议

- 确认后建议隔离（移动到 quarantine 目录）而非直接删除
- 记录文件 hash: `md5sum <file>`
- 检查同目录其他文件和访问日志

## 输出格式

```
## Webshell 狩猎报告
- 扫描范围: [目录路径]
- 可疑文件: [路径 | 修改时间 | 风险特征]
- 确认木马: [路径 | 类型 | 内容摘要]
- 建议措施: [隔离/删除/监控]
```

## 注意事项

- jQuery/CMS 核心文件常有 eval，需结合路径判断
- 优先行为分析（mtime）而非纯签名
- 高危删除操作需用户确认
