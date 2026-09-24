---
name: find-skills
description: 跨 ClawHub 和 skills.sh 发现、比较、检查、审查并按用户明确授权安装 Agent Skill。用户询问“有没有某种技能”“找一个 Skill”“从市场安装/复刻 Skill”或希望扩展能力时使用；搜索不等于安装授权。
license: Apache-2.0
---

# Find Skills

使用 DeepSentry 原生 `skill_market` 工具发现兼容 `SKILL.md` 的第三方能力。不要用 Shell 调用 `npx`、`clawhub`、`curl | sh`，也不要直接复制未知仓库；原生工具会限制市场域名、下载体积、文件数量和路径，并在安装前做静态审查。

## 工作流

1. 从用户目标中提取具体领域和任务，先搜索，不安装：

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"search","query":"linux log forensics","market":"all","limit":"8"}}
```

2. 比较候选时优先考虑：

   - 官方或声誉明确的作者；
   - 安装/下载量与近期维护情况；
   - 描述与当前任务是否精确匹配；
   - 是否包含脚本、外部服务、凭据或系统修改要求；
   - 是否与已有 Skill 重名或功能重复。

3. 推荐前至少检查一个候选。ClawHub 可查看完整元数据、版本、市场安全状态和 `SKILL.md` 预览：

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"inspect","source":"clawhub:security-audit"}}
```

skills.sh 结果使用 `skills:<owner>/<repo>@<skill>`，安装时只下载匹配的 Skill 子目录。

4. 清楚告诉用户候选来源、用途、流行度、风险和安装引用。仅当用户明确说“安装/下载/添加这个 Skill”后才安装；确认不能从一次普通搜索或能力询问中推断。

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"install","source":"skills:owner/repo@skill-name","confirm_install":"true"}}
```

5. 如果市场将候选标为可疑，或下载后的本地静态审查发现管道执行、密钥读取、强制删除或持久化等模式，先展示原因并请求用户重新确认。只有收到针对该风险的明确同意后，才可传 `acknowledge_risk=true`。市场标为恶意的 Skill 永远不得安装。

6. 安装完成后报告安装目录、来源版本/提交、SHA-256 和静态审查结果。新会话会自动发现；当前会话需要时使用 `load_skill` 或 `/skill load <name>`。

## 维护、更新与回滚

列出由市场管理的 Skill：

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"managed"}}
```

审查用户级 Skill：

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"audit"}}
```

先只读检查更新，再经用户明确授权更新：

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"check_updates"}}
```

```json
{"action":"tool","tool_name":"skill_market","tool_args":{"action":"update","name":"log-audit","confirm_update":"true"}}
```

需要稳定复现时使用 `pin` 冻结当前版本，解除时用 `unpin`。覆盖安装和更新都会保留旧版本；`rollback` 必须带 `confirm_rollback=true`。`uninstall` 必须带 `confirm_remove=true`，默认移动到可恢复备份，不永久删除。

静态审查只是供应链防线之一，不证明 Skill 的业务逻辑安全。遇到会执行脚本、读取密钥、联网、修改持久化配置或删除文件的工作流，仍应在实际执行相应动作时按 DeepSentry 风险确认流程处理。

## 无结果时

尝试更具体的同义词，或分别搜索 `clawhub`、`skills.sh`。仍无合适结果时，说明没有找到可信候选，并建议直接完成任务或创建一个最小、可审查的新 Skill；不要为了“有结果”推荐低质量或不相关项目。
