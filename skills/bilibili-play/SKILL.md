---
name: bilibili-play
description: >-
  用已连接的 HawkEye MCP 在真实浏览器打开并播放 B站/哔哩哔哩视频（含搜索、合集第N集 ?p=N、
  倍速 video.playbackRate、真实全屏 press_key F、GPU 黑屏时 canvas.drawImage 抓帧）。
  用户说打开/播放/看 B站、全屏、倍速时必须先 load_skill 本 skill。这是直播放，不是下载。
license: Apache-2.0
---

# 在真实浏览器播放 B站视频（倍速 / 全屏 / 抓帧）

这是 **HawkEye 真实浏览器直播放** 工作流，不是下载，也不是内置 `browser_browse`。
HawkEye 已出现在本会话工具列表中就等于已经连通，禁止再做 MCP 安装/端口/MD5 自检。

工具名以本会话实际前缀为准（常见 `hx0-hawkeye__browser_navigate`）。下文写短名。

## 何时使用

用户要在浏览器里 **看/播放** B站视频，尤其带全屏、倍速、第 N 集。典型说法：
「用MCP打开B站播放奥特曼」「在B站看/播放」「2倍速全屏」。

## 禁止事项

- 禁止内置 `browser_browse` / `browser_interact`。
- 禁止反复 `browser_tabs action=new`；已知 URL 用 `browser_navigate`。
- 禁止靠连点播放器控件碰运气设倍速或全屏。
- 禁止点击屏幕上的「全屏」按钮当主路径。
- 禁止对 snapshot artifact 做 `read_file`。
- 全屏 GPU 层导致 `browser_screenshot` 黑屏时，禁止反复截图；改用 canvas 抓帧。

## 工作流

### 第 0 步 — 本 skill 已加载后直接做用户任务

不要先检查扩展、端口或文件哈希。

### 第 1 步 — 找到视频

- 用户给了 URL：`browser_navigate` 直达。
- 只有关键词：navigate 到 `https://search.bilibili.com/all?keyword=<url-encoded>`。
- 搜索快照里选一条 `href` 以 `/video/BV...` 开头的结果，再 **navigate 到完整**
  `https://www.bilibili.com/video/BV.../`。**优先按 URL 导航，不要点搜索卡片**（点击容易让标签超时）。

合集 / 分 P /「第 N 集」：navigate 到 `https://www.bilibili.com/video/BV...?p=N`，
或在播放列表点对应分 P。用页面 title 或 URL 含 `?p=N` 确认。

整次任务最多 `browser_tabs action=new` 一次。

### 第 2 步 — 确认正在播放

`browser_snapshot` 看到播放器控件即可。用 `hawkeye_evaluate` 读 video，不要盲点播放键：

```js
(() => { const v = document.querySelector('video'); return v ? {paused:v.paused, cur:v.currentTime, dur:v.duration, rate:v.playbackRate} : {found:false}; })()
```

`paused === true` 时再 `document.querySelector('video').play()`。
过几秒再读一次 `currentTime`，必须递增才算在播。

### 第 2b 步 — 倍速（用户明确要求时）

**主路径：直接设 `video.playbackRate`。不要点「倍速」菜单，也不要把 `browser_select_option` 当主路径。**
该属性在进入全屏后仍然有效。

```js
(() => { const v = document.querySelector('video'); if (v) v.playbackRate = 2; return { rate: v && v.playbackRate }; })()
```

返回 `rate: 2` 即成功。清晰度下拉才用 `browser_select_option`。

### 第 3 步 — 真实全屏

全屏需要真实用户手势（`navigator.userActivation`）。

**主路径：** 先聚焦播放器，再 `browser_press_key key=f inputMode=trusted`。
未传 `inputMode` 时 DeepSentry 会自动补 `trusted`。

随后 `hawkeye_evaluate` 校验：

```js
(() => ({ fs: !!document.fullscreenElement, el: document.fullscreenElement && document.fullscreenElement.className, ua: (()=>{const u=navigator.userActivation;return u?{isActive:u.isActive}:null})() }))()
```

`fullscreenElement` 存在即成功（B站常见 `bpx-player-container`）。

**回退：** `press_key` 后 `userActivation.isActive` 仍为 false / Fullscreen request denied
时，在同一次 `hawkeye_evaluate` 里对播放器容器 `requestFullscreen()`（async IIFE，保持激活窗口）：

```js
(async () => {
  const v = document.querySelector('video');
  const target = v ? (v.closest('.bpx-player-container') || v.closest('.bpx-player-video-wrap') || v.parentElement) : document.documentElement;
  try {
    const r = target.requestFullscreen ? await target.requestFullscreen() : null;
    await new Promise(res => setTimeout(res, 200));
    return { ok: true, fullscreenActive: !!document.fullscreenElement, q: !!r };
  } catch (e) {
    return { ok: false, error: String(e && e.message || e) };
  }
})()
```

trusted 失败时 **不得** 用 `dispatchEvent` 伪装成功，也不得连点「全屏」按钮。

### 第 4 步 — 全屏后若暂停则恢复

```js
(() => { const v = document.querySelector('video'); if (v && v.paused) v.play(); return { paused: v && v.paused, rate: v && v.playbackRate, cur: v && v.currentTime }; })()
```

### 第 5 步 — 确认画面（黑屏陷阱）

`browser_screenshot` 在 B站原生全屏时常返回黑帧：视频在 GPU 硬件层上，截图合成器看不见。
这 **不是** 没在播。先用 evaluate 确认 `paused:false` 且 `currentTime` 在递增。

需要可见画面时，用页面 canvas 抓当前帧，不要反复 `browser_screenshot`：

```js
(async () => {
  const v = document.querySelector('video'); if(!v || v.readyState<2) return {found:false};
  const w=960, h=Math.round(v.videoHeight*(w/v.videoWidth));
  const c=document.createElement('canvas'); c.width=w; c.height=h;
  const ctx=c.getContext('2d'); ctx.drawImage(v,0,0,w,h);
  const data=c.toDataURL('image/jpeg', 0.7);
  return {ok:true, data:data, w, h};
})()
```

`ok:true` 且 `data` 为 JPEG data URL 即成功。字符串很大时工具可能落盘 spill，从最长
`data:image/jpeg;base64,...` 解码成 `.jpg`。用 `read_image` 看水印/字幕确认是对的片源。

若用户可以退出全屏，退出后再 `browser_screenshot` 也能拍到画面；全屏直播放仍以 canvas 为准。

## 必须记住

- 倍速 = `video.playbackRate`，不是点菜单。
- 全屏 = `press_key f` + `fullscreenElement`，不是点「全屏」。
- 合集第 N 集 = `?p=N` 或点分 P。
- 黑屏截图 ≠ 播放失败；`currentTime` 递增才是在播。
- 精确点击（搜结果以外）必须 `text=无障碍名称` + `ref`。
- 本 skill 不管下载；下载是另一件事。
