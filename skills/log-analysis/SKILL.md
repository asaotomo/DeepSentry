---
name: log-analysis
description: 日志取证与威胁关联 — 从 auth/syslog/web 日志中提取攻击 IP、失败登录、异常行为
license: Apache-2.0
---

# 日志分析 Skill

## 何时使用

- 用户要求分析登录日志、Web 访问日志、系统日志
- 需要找出攻击源 IP、暴力破解、异常登录时间
- 需要关联 IP 归属地或威胁情报

## 工作流程

1. **确认日志路径**
   - Linux: `/var/log/auth.log`, `/var/log/secure`, `/var/log/nginx/access.log`
   - Windows: `C:\Windows\System32\LogFiles\`

2. **提取关键事件**
   ```bash
   # SSH 失败登录 Top 10
   grep "Failed password" /var/log/auth.log | awk '{print $(NF-3)}' | sort | uniq -c | sort -rn | head -10

   # 最近 24 小时失败登录
   grep "$(date +%b\ %d)" /var/log/auth.log | grep -i "failed\|invalid"
   ```

3. **时间线重建**
   - 按时间排序关键事件
   - 识别攻击窗口（集中失败 -> 成功登录）

4. **威胁关联**
   - 对 Top IP 查询归属地: `curl ipinfo.io/<IP>`
   - 标记已知恶意 IP 段

## 输出格式

```
## 日志分析结论
- 分析范围: [日志文件 + 时间范围]
- 攻击 IP Top N: [IP | 次数 | 归属地]
- 关键事件时间线: [时间 | 事件 | IP]
- 风险等级: [高/中/低]
- 建议措施: [封禁/监控/加固]
```

## 注意事项

- 优先使用 grep/awk 而非全量 cat
- 大日志文件先用 tail/head 采样
- 禁止删除或修改日志文件
