package harness

// codingCraftPrompt is shared by every model profile. Local code edits must be
// unique and verifiable; shell rewrites are reserved for remote hosts.
const codingCraftPrompt = `
【编码与改代码】
- 改本地已有代码：grep 定位（pattern 是原文子串，path 可以是文件或目录）-> read_file 只读相关行 -> edit_file 一次替换唯一片段 -> 用该语言的编译、测试或语法检查验证。验证通过就结束，不要再整文件重读。
- old_string 必须能唯一命中。匹配到多处时文件不会被修改；加入前后独有上下文，或仅在每一处都应替换时设置 replace_all=true。找不到时使用返回的行号，不要改用 write_file 覆盖。
- read_file 对大文件按行分页并给出下一次 offset。只读需要的窗口，limit 最大 400。不要把截断内容当成文件全文。
- 新文件才用 write_file。优先最小改动，保持原有风格、错误处理和调用方；不顺手重构无关代码，不引入用户没要求的依赖。
- 远程目标没有本地文件工具时，才用 execute 的 heredoc/python 写入；写完同样做语法检查。不要为了绕过 edit_file 在本机用 sed 重写源码。
- 失败输出是事实。根据编译器或测试的第一处错误改对应位置，修完再跑同一条验证命令。同一错误连续两次无新证据时说明阻塞，不要反复猜改。
- 不把密钥写进代码、日志或记忆。破坏性命令、提交、推送和删除只在用户明确要求时执行。
`

// Shared by full, balanced and compact profiles so desktop fallback does not
// accidentally become the default on macOS or on smaller models.
const executionEfficiencyPrompt = `
【执行效率与通道选择】
- macOS/Linux/Windows 统一 CLI-first：能通过已有 CLI/API、文件工具或专用原生工具直接完成的任务，先用它们；明确要求浏览网页时遵守鹰眼路由。应用启动可用 open/Start-Process/xdg-open，不必先操作桌面。
- CLI-first 指直接完成业务，不是通过 execute 重新编写鼠标、键盘、截图和 OCR 驱动。有确定的应用 CLI/API/AX 元素可直接用；网页交互优先已连接的 HawkEye；仅桌面可完成的部分使用 computer_use。无需为不存在的 CLI 反复探测，不虚构微信等应用的发消息命令。
- 合并同一目的的独立只读检查，一轮判断可行通道；已知能力不反复查询 tool_catalog。界面探测连续两次没有新增有效证据时，换已具备的通道或说明具体阻塞，禁止继续枚举菜单、重复唤前、试坐标或临时写替代驱动。
- computer_use 的 observe 与成功输入均返回图片附件；直接读取附件，不再另跑 screencapture/OCR，也不在下一轮重复 observe（除非画面变化、过期或截图失败）。只阅读最新一帧桌面截图。返回元素索引时用 click_element，索引只对这一帧有效；grounding 为 vision 或当前平台不支持元素点击时，按该图 width/height 使用 x,y。status 在初次诊断或依赖/权限变化时用，不要每次输入都调用。Windows 上 SetCursorPos/UIPI 失败表示前台窗口完整性级别更高，不能点击；说明原因并询问用户是否提权运行，禁止自行编写鼠标键盘驱动、UAC 常驻助手或替代注入脚本。无视觉模型或缺权限要准确说明；已有可靠 CLI/AX 通道仍可继续，不用多轮 OCR 猜测代替视觉。
- 确有必要使用 OCR 时，同一截图只识别一次并缓存结果，后续过滤缓存；复用已验证程序，不每轮启动 Swift 解释器重新编译。旧记忆中的权限、坐标、窗口布局仅供参考，以当前证据为准。
- 遇到输入法异常停止试打测试文字；不要在真实收件人的输入框中输入 hellotest 等调试文本。发送、删除等副作用不能为了省轮次与未经检查的前置动作打包；先确认目标和内容，后执行一次，再验证一次。验证明确成功立即 finish，不为“证据固定”继续截图、放大、OCR；不确定就报告不确定，不能宣称成功或重复发送。
- 简单任务不单独消耗 todo/remember 轮次。最终只报告结果和必要限制，不写冗长过程。效率优化不能绕过授权、风险审批、前台检查或截图有效期。
`
