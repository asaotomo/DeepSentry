---
name: zipcracker
description: >-
  ZipCracker 兼容的控制端 ZIP 恢复。用户提到压缩包密码、伪加密、ZIP 爆破、CRC32、
  四位数字、字典/掩码、ZipCrypto、WinZip AES、打不开的 zip 时先用
  zip_password_recover，不要先 python/unzip。不要对 rar/7z 使用。
license: Apache-2.0
---

# ZipCracker（DeepSentry 原生，必须先走）

ZIP 解密/伪加密/打不开的**第一步**必须调用控制端内置 `zip_password_recover`。
禁止先 `inspect`、`pwd`、`ls`、`execute` Python ZipCracker、`unzip`/`7z`/`john`。
只有该工具明确失败（超时、未命中、不支持 KPA/ZIP64）后，才允许其他办法。

远程归档先 `file_download`，再立刻 `zip_password_recover`。

## 第一步（不要分步探路）

用户给了 `-m` / 掩码时：

```json
{"action":"tool","tool_name":"zip_password_recover","tool_args":{"action":"recover","source":"/绝对路径/file.zip","mask":"?uali?s?d?d?d","extract":"true"}}
```

没给掩码时（伪加密 / 弱口令 / 短明文 CRC32 都走这条）：

```json
{"action":"tool","tool_name":"zip_password_recover","tool_args":{"action":"auto","source":"/绝对路径/file.zip","extract":"true"}}
```

`auto` 已内置 ZipCracker 顺序：伪加密修复 → 短明文 CRC32 → 用户 mask（若有）→ 内置 6000 字典 → 1-6 位数字。不要先 `inspect` 再决定。

有字典文件/目录就加 `dictionary`。`?s` 与 ZipCracker 一样是 Python `string.punctuation`。掩码必须原样传递。

## 已复刻 / 未复刻

已复刻：伪加密修复（含 data descriptor 位）、1～6 字节 CRC32（命中的是**文件内容**不是口令）、内置约 6000 字典、1-6 位数字、字典目录、掩码 `?d/?l/?u/?s/??`、ZipCrypto / WinZip AES、成功后安全解压。

未复刻，原生失败后如实说明，不要假装做过：已知明文 / `bkcrack` / 模板 KPA、ZIP64、分卷 ZIP、RAR / 7z。

## 规则

- 每次调用都要带 `source`。任务里出现了 `*.zip` 就用那个路径。
- 原生成功后再 `ls`/`read_file` 看解压目录。已经看到 `flag.txt` / `flag{...}` 就 `finish` 提交，禁止把 flag 或口令当作 `execute` 的 command。
- 不要把 CRC32 十六进制值当密码去 `recover`。
- WinZip AES 只走字典/掩码。
