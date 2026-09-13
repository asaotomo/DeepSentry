# DeepSentry「AI 智运」赛前手册

## 1. 已确认赛制

以 2026-07-25 比赛通知为唯一题型事实来源：

- 比赛时间 9:15–10:15，6 题，每题 10 分钟。
- 机房管理 A1/A2/A3：15/15/20 分。
- 网络类 B1/B2/B3：15/15/20 分。
- 评分：任务完成度 40% + 技术准确性 30% + AI 应用效率 20% + 输出规范 10%。
- 识别 AI 幻觉并纠正 +2 分/题，上限 5 分；直接复制未验证的错误 AI 答案 -3 分/题。
- 题目为模拟配置和模拟日志，禁止连接生产环境。
- 允许互联网与国产 AI 工具，严禁互相交流和远程协助。

通知未公布具体题干。任何“原题/题库”说法在拿到官方材料前都必须标记为推测。

## 2. 可能考点（赛前推测，非题库）

| 模块 | 高概率能力 | DeepSentry 对应快速路径 |
| --- | --- | --- |
| 机房 A1 | CPU/内存/磁盘/进程/服务异常的基础判断 | `host_incident_baseline` |
| 机房 A2 | 日志时间线、告警联动、根因和影响范围 | `read_log` / `file_tail` / `log-analysis` |
| 机房 A3 | 复杂故障的处置、复验、回滚，或 UPS/温湿度/能耗类表格数据 | `document_parse` + `competition_answer_check` |
| 网络 B1 | 物理端口、错包、速率/双工、流量利用率 | `network_device_diagnose focus=interfaces` |
| 网络 B2 | VLAN/STP/MAC/环路、端口收敛 | `network_device_diagnose focus=l2` |
| 网络 B3 | 路由表、OSPF 邻居、路由可达性、配置差异与修复复验 | `network_device_diagnose focus=routing` |

为什么这样推测：15 分题更适合单域快速定位，20 分题更可能要求“定位 + 处置 + 复验 + 规范输出”的完整闭环。这是根据分值的推断，不是官方透题。

## 3. 每题 10 分钟节奏

1. 0:00–1:00：拆出题干所有交付项，明确是只读分析还是需要处置。
2. 1:00–4:00：运行一个最相关的确定性快诊，不做全量漫游。
3. 4:00–6:00：对异常补 1–3 个针对性命令，交叉验证根因。
4. 6:00–8:00：给出最小处置；如实际执行修改，保留原配置/回滚点。
5. 8:00–9:00：用状态、连通性、日志或计数器复验。
6. 9:00–10:00：生成固定格式答案；20 分题用 `competition_answer_check` 自检一次。

## 4. 交换机“截断”判断

- `projection=filtered, output_truncated=false`：传输完整，但设备只返回 `include/exclude/begin/section` 匹配行。VRP/Comware 默认区分大小写。
- `output_truncated=true`：真正超过 DeepSentry 输出字节上限；运行时仍会排空设备输出并找回 prompt，避免污染下一条命令。
- 超时或 `prompt_seen=false`：会话边界未恢复，可能是分页样式未识别、prompt 配置错误、设备忙或链路中断。
- TUI 中的“`e` 展开完整结果”：只是界面折叠，不是给模型的工具结果被截断。

单接口详细诊断直接执行：

```text
display interface GigabitEthernet2/1/2
```

不要第一步就用：

```text
display interface GigabitEthernet2/1/2 | include rate|packets|bytes|bandwidth|utilization|last|input|output
```

后者是行过滤投影，会丢掉端口状态、错包类型和关联上下文；并且 `Input` / `Output` 的大小写差异会导致部分行不匹配。

## 5. 使用方式

```bash
./deepsentry --competition --task "将官方题干和模拟日志/配置完整粘贴到这里"
```

网络设备模拟环境如提供 SSH，显式配置厂商可节省自动探测时间：

```yaml
target_protocol: ssh
ssh_device_type: huawei # 或 h3c / ruijie / cisco
ssh_prompt: ""          # 可空自动探测
```

如题目只给模拟日志/配置文件，使用本地模式分析，不得为了“验证”连接生产网络。

## 6. 仍需赛前演练的缺口

- 机房物理环境（UPS、电池、温湿度、制冷、PUE）不宜使用固定通用阈值；必须以题干阈值、厂商规范或数据的同期基线为证据。
- 锐捷/Cisco/H3C 不同版本的命令存在差异；命令报不支持时要记录为“能力差异”，不要解读为“未发现异常”。
- 比赛前至少用 6 个离线 fixture 各演练一次，重点记录每题总耗时、重复命令数、无证据结论数和格式漏项。
