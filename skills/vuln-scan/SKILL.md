---
name: vuln-scan
description: 系统脆弱性基线扫描 — 检查空口令、特权账户、防火墙、开放端口、SUID 文件、计划任务
license: Apache-2.0
---

# 漏洞扫描 Skill

## 何时使用

- 用户要求系统安全基线检查
- 新服务器上线前的安全评估
- 合规审计（等保/CIS Benchmark）

## 检查清单

### 1. 账户安全

```bash
# UID=0 的非 root 账户
awk -F: '$3==0 && $1!="root" {print}' /etc/passwd

# 空口令账户
awk -F: '($2=="" || $2=="!") {print $1}' /etc/shadow 2>/dev/null

# 可登录用户
grep -v nologin /etc/passwd | grep -v false
```

### 2. 网络暴露

```bash
# 监听端口
ss -tlnp 2>/dev/null || netstat -tlnp

# 防火墙状态
iptables -L -n 2>/dev/null || ufw status 2>/dev/null

# 高危端口: 21, 23, 445, 3389, 6379, 27017
```

### 3. 文件权限

```bash
# SUID 文件
find / -perm -4000 -type f 2>/dev/null | head -20

# 全局可写目录
find / -type d -perm -o+w 2>/dev/null | head -10
```

### 4. 计划任务

```bash
crontab -l 2>/dev/null
cat /etc/crontab 2>/dev/null
ls -la /etc/cron.* 2>/dev/null
```

## 输出格式

按风险等级分类：

```
## 脆弱性扫描报告
### 🔴 高风险
- [发现项 | 详情 | 修复建议]

### 🟡 中风险
- [发现项 | 详情 | 修复建议]

### 🟢 低风险 / 信息
- [发现项 | 详情]
```

## 注意事项

- shadow 文件读取可能需要 root
- 禁止执行修复操作，仅报告
- Windows 环境使用 wmic/netsh 替代
