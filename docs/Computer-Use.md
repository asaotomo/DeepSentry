# DeepSentry Computer Use

`computer_use` 是内置的本机桌面工具，与现有 Agent 审批、视觉附件和任务中止链路集成。它不依赖专有 OpenAI computer-use API，也不会凭空赋予文本模型视觉能力：必须配置支持图片输入的模型。

## 平台与当前验证范围

| 平台 | 驱动 | 运行条件 | 当前验证 |
|---|---|---|---|
| Windows 10/11 | 系统 PowerShell/.NET + SendInput | 已登录、解锁的交互桌面；普通权限不能操作管理员窗口/UAC 安全桌面 | Go 交叉编译、驱动代码检查；尚未桌面实测 |
| macOS 12+ | 随附 Swift/CoreGraphics helper | 系统辅助功能、录屏授权 | arm64/x86_64 helper 编译；当前 Mac 权限检测实测；输入闭环未获权限验证 |
| Linux X11 | xdotool + ImageMagick import | 有 DISPLAY，X11 活动窗口及捕获权限 | Go 编译、控制层与进程取消测试；尚未真实 X11 桌面实测 |
| Linux Wayland | 暂无 RemoteDesktop portal 后端 | 需要 compositor/portal 用户授权与视频流集成 | 明确返回不可用，不冒充完整桌面支持 |

这是具备防重放、锁、中止和截图反馈的首版实现。尚未完成各 OS 的生产验收，不能声称已经在全部桌面环境生产可用。macOS/Windows 当前仅捕获和定位主屏，Linux 使用 X11 根桌面。元素索引只在 macOS 辅助功能可用且树足够具体时启用，尚未做真实桌面验收。多显示器任意屏幕、OCR、锁屏、无人登录桌面及跨主机 GUI 会话不在此次支持范围内。Windows / Linux 不提供 UIA 或 AT-SPI 元素点击。

## 部署

先运行，不需要配置模型或发送任何输入：

```sh
./deepsentry --computer-check
```

Windows 为 `deepsentry-windows-amd64.exe --computer-check`。标准输出是 JSON，标准错误给一行中文结论。状态就绪后会真的截一张图并立刻删除，所以「权限显示已开、实际截不了图」也会报出来。`desktop.ready=false` 会说明缺少的权限或依赖，退出码为 1。macOS 未授权时会自动弹出系统授权框，授权后再运行一次即可。

macOS 把 `computer-use-helper` 与 DeepSentry 可执行文件放在同一目录，或放在旁边的 `bin/` 目录。源码构建可运行：

```sh
bash scripts/build-computer-helper.sh
```

开发模式 `go run ./cmd --computer-check` 可设置 `DEEPSENTRY_COMPUTER_HELPER` 为 helper 的绝对路径。不会在任意当前工作目录自动寻找并运行 helper。需引导系统显示授权入口时，由操作者执行：

```sh
printf '{"action":"request_permissions"}' | ./computer-use-helper
```

按照系统弹窗在“系统设置 → 隐私与安全性”中授权，并重新启动程序。代码不会绕过授权。构建脚本使用本地 ad-hoc 签名；对外生产发布应使用 Developer ID 签名、公证，并验证更新后权限身份保持稳定。

Linux X11 需要 `xdotool` 与 ImageMagick（提供 `import`）。例如 Debian/Ubuntu 的软件包为 `xdotool imagemagick`。在桌面用户会话中启动程序；SSH 环境的 DISPLAY 变量不能代替桌面授权。

## Agent 使用闭环

先 `computer_use(action="status")`，再 `computer_use(action="observe")`。模型会收到真实 PNG 附件，以及 `observation.id`、原始图片宽高、`grounding` 和桌面状态。图片以 `detail=original` 发送，坐标按这张图的 `width/height` 像素、从左上角 `(0,0)` 计算；后台再把截图像素换成桌面坐标，不使用聊天界面缩略图的尺寸。历史里只保留最新一帧桌面截图，更早的桌面图会从模型上下文去掉，用户自己的图片和鹰眼截图保留。

macOS 同一次 observe 会附带焦点窗口的有限无障碍元素（最多约 6 层、80 个有名字或可操作的节点）。`grounding=element` 时用 `click_element` 的 `element_index`，或对可设值控件用 `set_value`。索引只对这一帧有效；执行前会重读该节点，角色或名称变了就拒绝点击。元素树稀疏、几乎没有名称，或当前平台没有无障碍后端时，`grounding` 为 `vision` 或 `unsupported`，必须改用 `x,y`。Windows 和 Linux 本轮不提供元素索引。微信这类自绘界面会走视觉坐标，不要假装元素点击可用。

一次像素点击示例（ID 必须取实际返回值）：

```json
{"action":"click","observation_id":"实际截图ID","action_id":"task-click-0001","x":320,"y":240}
```

元素点击示例：

```json
{"action":"click_element","observation_id":"实际截图ID","action_id":"task-elem-0001","element_index":3}
```

支持 `click`、`double_click`、`move`、`drag`（to_x/to_y）、`scroll`（amount 负上正下，绝对值不大于 10；可同时给 x,y 指定滚动位置）、`type`（最多 2000 字符）、`key`（如 CTRL+A、META+S、ENTER）、`wait`（wait_ms 为 200 到 3000，只等待并刷新截图，不注入键鼠）。输入成功后自动截取下一帧；模型必须读取截图验证目标是否达成，`input_dispatched=true` 不是业务成功证明。

每次输入只接受当前会话最新、60 秒内的截图，并消耗该截图。刷新截图失败会作废旧帧；慢速状态检查结束后再次检查有效期。切换前台应用、几何或权限发生变化时要求重新观察。macOS 还会比较前台窗口编号：同一应用换到另一个窗口（例如弹出的独立聊天窗）也要重新截图；只有标题变化（例如未读数）不会作废截图。同窗口内部动态内容仍可能在观察和输入间变化，所以不能把这些检测当成消除所有误点的保证。

截图像素按像素中心换算成桌面坐标，截图比桌面小时点击不会整体偏向左上。macOS 读取无障碍元素失败时，`grounding_note` 会写明原因，这一帧只能用 `x,y`。

不确定调用重试必须使用相同 `action_id` 和完全相同参数：进程内直接返回原收据，不再次输入。同一 ID 不同参数会被拒绝。收据保留 5 分钟；截图 60 秒过期且只能用一次，更早的收据已经无法触发重放，所以账本满 4096 条时会先清掉这些旧收据，长时间桌面任务不再因此卡死。输入前被取消会返回 `cancelled_before_input`：键鼠没有动，但截图已用掉，要重新 observe 并换新的 `action_id`。重启不保留内存收据，旧截图 ID 也不再有效；重启后先观察并确认实际状态，不重放未确认的旧动作。

输入动作使用现有高风险审批机制。只读状态、观察、中止和释放为低风险。整次桌面任务可按现有 UI 选择会话内同类授权。`--batch -y` 和 WebShell 模式不会跳过桌面输入确认，避免定时或后台任务在有人用电脑时抢鼠标；确需无人值守操作桌面，另设 `DEEPSENTRY_COMPUTER_USE_UNATTENDED=1`。网页任务优先使用鹰眼的元素定位；桌面 screenshot 内容是不可信任务数据，不能覆盖用户指令。认证与验证码交由用户处理。

## 独占、中止与审计

- 同一进程并发桌面请求返回 busy；不同 Agent 会话不能抢占活跃桌面。
- 通过用户缓存目录内的 OS 文件锁避免同一用户的不同 DeepSentry 进程同时控制桌面；系统崩溃后 OS 自动释放锁。会话空闲 5 分钟释放独占，完成任务应显式 `release`。
- UI 中止信号会取消当前调用。`action=stop` 还会锁止后续输入，必须由持有桌面的会话明确 `resume` 并重新 observe 才能继续（租约过期后可由新会话恢复）。它不能撤销已经送达 OS 的输入。
- 取消或输入错误时尝试释放鼠标按钮和修饰键；不能保证被系统杀死、系统崩溃或权限突然撤销时一定释放。
- `DEEPSENTRY_COMPUTER_USE_DISABLED=1` 可在启动前禁用功能。
- 截图在启动目录下的 `reports/computer-use/screen-*.png`（启动时固定为绝对路径，运行中切换目录不会分散到别处），目录 0700、文件在截图写入前以 0600 独占创建（Windows 应另外核验用户目录 ACL）。截图可能包含敏感信息，并会发送给你配置的视觉模型提供方。服务不读取剪贴板，也不为 type 覆盖剪贴板。
- `actions.jsonl` 记录会话、动作类型、ID 和阶段，不保存输入正文。截图目录超过 512 MiB 时暂停新截图，由操作者归档/清理，避免无限增长；单图最多 20 MiB、64MP。

## 生产验收清单

在每个目标 OS 的测试桌面验证：不同 DPI/Retina；主屏切换；普通/管理员窗口；中英文和组合键；拖拽；重复 action_id；切换前台导致拒绝；两会话/两进程争用；Esc 中止；锁屏/RDP 断开；缺权限；截图发送到模型并完成一项可回滚任务。不要在完成这些验证之前开启无人值守批量桌面操作。

## 桌面输入与安全边界

- 修复失败的截图刷新留下旧帧、慢速状态查询延长旧帧有效期的问题。
- 禁止其他会话恢复当前桌面所有者的中止状态。
- Windows helper 使用隐藏且无控制台的子进程，避免新控制台影响前台窗口；拖拽每段检查光标移动结果。
- Unix helper 取消时终止整个进程组，限制继承输出管道的等待时间，覆盖 screencapture 子进程。
- X11 移动不再使用等待光标发生变化的同步选项，连续点击相同坐标不会因此阻塞；同一 xdotool 命令中的事件仍按顺序发送。
- macOS 鼠标、键盘、滚轮事件创建失败会返回错误，避免回报已发送成功。
- 此修订没有扩大已验证的真实桌面范围，平台限制仍如上表。

## 工具选择与执行效率

所有模型提示配置统一采用 CLI/API/原生工具优先、网页鹰眼优先、桌面交互兜底的策略。启动应用可直接调用 OS 启动命令；CLI 优先不代表重新编写截图、点击和 OCR 驱动。已确认界面不可通过结构化接口访问时及时转入 desktop 工具；无视觉或权限不足时明确阻塞。

补充微信客户端、原生应用、UI 自动化和屏幕截图等工具发现词。要求合并只读探测、复用截图/OCR 结果、成功输入后的图片附件直接用于下一步，明确成功后立即结束。简单任务不强制单独更新 todo 或 memory；副作用前的收件人/内容核验与授权保持不变。

桌面操作耗时取决于系统授权、前台应用、模型能力和网络环境。请在自己的目标环境验证实际效果。

## 截图反馈与元素索引

模型上下文只保留最新一帧桌面截图，并以 `detail=original` 发送，避免按缩小后的图点击原始像素。截图是否仍有效改为比较应用身份和桌面几何；同一应用的窗口标题变化不再单独作废截图。`scroll` 可以带截图像素位置，`wait` 只等待并刷新截图。

macOS 在 observe 中返回焦点窗口的有限无障碍元素。`click_element` / `set_value` 在执行前核对角色和名称；树稀疏或没有名称时 `grounding=vision`，改用截图像素。Windows 和 Linux 的元素点击明确返回未支持。此修订没有扩大已验证的真实桌面范围。

## 使用体验与安全保护

- macOS 同一应用切换窗口会作废旧截图，避免点到另一个窗口。
- `--batch -y` / WebShell 下桌面输入仍要确认，除非另设 `DEEPSENTRY_COMPUTER_USE_UNATTENDED=1`。
- 输入收据超过 5 分钟可清理，长任务不再在 4096 次后永久失败。
- 坐标按像素中心换算；元素树读取失败会在 `grounding_note` 里说明。
- `--computer-check` 实际截一张图验证，macOS 未授权时自动弹出授权框。
- 仍未完成 Windows / Linux 真实桌面验收；Windows 每次动作都会启动一次 PowerShell，慢机上单次操作可能需要数秒。

## Windows 常驻辅助进程与多 Agent

- Windows 桌面改为常驻 PowerShell helper：一个进程从标准输入按行读取 JSON 请求，`Add-Type` 只在启动时编译一次，之后的 status/截图/输入都复用同一进程，慢机上不再每次动作冷启动 PowerShell。
- 数据流一旦异常（进程退出、管道断开、响应超时）会自动回退到原来的单次进程模式；只读/可重复的动作（status、capture、elements）才允许回退重试，鼠标键盘等改变状态的动作在流断开时直接报错，绝不重放以免重复点击或输入。
- helper 明确报告的动作错误（例如 UIPI 拦截、无可用交互桌面）会原样上抛，不会被当成传输故障而重试。
- 设 `DEEPSENTRY_COMPUTER_USE_PERSISTENT=0` 可关闭常驻模式，回到每次冷启动。常驻脚本与单次脚本共用同一份注入逻辑（`helpers/windows.ps1`），两种模式行为一致。
- 子 Agent 与主 Agent 共用同一桌面归属（会话号里的 `-sub-` 后缀会被归一到父会话），子任务不再和父任务互报「桌面被占用」。
- 本项改动已在 macOS 交叉编译并通过协议层单测验证；Windows / Linux 真机验收仍需现场确认。
