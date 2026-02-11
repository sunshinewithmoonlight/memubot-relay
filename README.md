# memubot-gemini-relay (memU bot 专用版)

这是一个高效、轻量的 Go 语言中继服务器，旨在让 **memU bot** 能够无缝使用 **Google Gemini API**。它通过将标准的 OpenAI 或 Anthropic (Claude) 请求协议转译为 Gemini 原生格式，解决了协议不兼容的问题。

## ✨ 特性

- **memU bot 深度适配**: 自动处理 memU bot 发出的 `/v1/messages` (Anthropic) 或 `/v1/chat/completions` (OpenAI) 请求。
- **协议转换**: 将各种 API 格式的消息流完整映射至 Gemini `generateContent` 接口。
- **🔧 Function Call 支持**: 完整支持 Anthropic/MiniMax 风格的工具调用（`tool_use`/`tool_result`）。
- **🧠 Thinking Mode**: 支持 Gemini 2.0 的思考模式，自动处理 `thought_signature`。
- **📦 上下文缓存**: 通过 `--cache` 参数启用。自动缓存 System Prompt 和 Tools 定义，减少网络传输和 API 成本。
- **内置代理**: 支持 `--proxy` 参数，方便在中国大陆等网络环境下通过本地代理访问 Google 服务。
- **TPM 速率限制**: 支持 `--tpm` 参数 (如 `0.9M`)，通过令牌桶算法平滑限制请求速率，防止触发 API 频率限制。
- **极简运行**: 无需配置复杂的环境变量，启动即用。

## ⚙️ memU bot 配置指南

在 memU bot 的设置界面中，请按下图进行配置：

| 配置项 | 内容 |
| :--- | :--- |
| **LLM 提供商** | `Custom Provider` |
| **API 地址** | `http://127.0.0.1:6300/` |
| **API 密钥** | `你的 Google Gemini API Key` |
| **模型名称** | `gemini-3-flash-preview` (或其它 Gemini 模型) |

## 🚀 快速开始

### 运行
**基本运行**:
```bash
./memobot-gemini-relay
```

windows 直接运行 memubot-gemini-relay-windows.exe

**使用代理运行**:
```bash
./memobot-gemini-relay --proxy http://127.0.0.1:7890
```

**启用 TPM 速率限制 (防止 429 错误)**:
```bash
./memobot-gemini-relay --tpm 0.9M  # 限制为 90万 tokens/分钟
./memobot-gemini-relay --tpm 1000000 # 限制为 100万 tokens/分钟
```

**启用上下文缓存 (减少传输量与 API 成本)**:
```bash
./memobot-gemini-relay --cache
# 按 Ctrl+C 可优雅退出并自动清理缓存
```

**调试模式 (查看详细数据包)**:
```bash
./memobot-gemini-relay --debug
```

### go环境运行
```bash
go run memubot-gemini-relay.go
```

### 编译
```bash
go mod init memubot-gemini-relay && go build -o memubot-gemini-relay . && rm go.mod
GOOS=windows GOARCH=amd64 go build -o memubot-gemini-relay-windows.exe memubot-gemini-relay.go
```

## 🔧 Function Call 支持

支持 Anthropic/MiniMax 风格的工具定义：
```json
{"name": "bash", "description": "...", "input_schema": {...}}
```

### 可用工具清单（19个）

| # | 工具名称 | 描述 |
|---|---------|------|
| 1 | `bash` | 执行 bash 命令 |
| 2 | `str_replace_editor` | 文件查看/编辑（view/create/str_replace/insert） |
| 3 | `download_file` | 从 URL 下载文件 |
| 4 | `web_search` | 网络搜索（Tavily AI） |
| 5 | `macos_launch_app` | 启动 macOS 应用 |
| 6 | `macos_mail` | Apple Mail 邮件操作 |
| 7 | `macos_calendar` | Apple Calendar 日历操作 |
| 8 | `macos_contacts` | Apple Contacts 联系人查询 |
| 9 | `feishu_send_text` | 发送飞书文本消息 |
| 10 | `feishu_send_image` | 发送飞书图片 |
| 11 | `feishu_send_file` | 发送飞书文件 |
| 12 | `feishu_send_card` | 发送飞书消息卡片 |
| 13 | `feishu_delete_chat_history` | 删除飞书聊天记录 |
| 14 | `service_create` | 创建后台服务 |
| 15 | `service_list` | 列出所有服务 |
| 16 | `service_start` | 启动服务 |
| 17 | `service_stop` | 停止服务 |
| 18 | `service_delete` | 删除服务 |
| 19 | `service_get_info` | 获取服务信息 |

### 测试 Prompt

| 测试工具 | Prompt |
| :--- | :--- |
| `bash` | `看看我的桌面上有什么文件` |
| `str_replace_editor` | `帮我在桌面创建一个 test.txt 文件，内容是 "Hello!"` |
| `web_search` | `搜索一下今天的天气怎么样` |
| `download_file` | `下载这个图片保存到桌面：https://example.com/image.png` |
| `macos_launch_app` | `打开日历应用` |
| `macos_contacts` | `在通讯录里搜索有没有叫"张三"的联系人` |
| `macos_mail` | `看看我的邮箱有多少封未读邮件` |
| `feishu_send_text` | `给我发一条消息说 "测试成功！"` |
| `feishu_send_card` | `发一个绿色的消息卡片，标题是"测试报告"` |
| `service_list` | `列出我现在有哪些后台服务在运行` |
| 组合测试 | `看看桌面上的 test.txt 内容，然后通过飞书发给我` |

### 注意事项

1. **新对话开始测试**：建议清空对话历史后重新开始，确保 `thought_signature` 正确传递
2. **Thinking Mode**：Gemini 2.0 的函数调用需要 `thought_signature`，本 relay 会自动缓存和恢复
3. **调试模式**：使用 `--debug` 查看完整的请求/响应数据

## 📦 上下文缓存

> [!IMPORTANT]
> 上下文缓存默认**关闭**，开启后可能会导致额外的缓存费用，但会减少 token 计费。需通过 `--cache` 参数启用：
> ```bash
> ./memobot-gemini-relay --cache
> ```

本中继实现了 [Gemini Explicit Context Caching](https://ai.google.dev/gemini-api/docs/caching)，启用后自动缓存 System Prompt 和 Tools 定义。

### 收益

| 维度 | 效果 |
|------|------|
| **网络传输** | 后续请求仅发送新消息，传输量减少 ~70% |
| **响应延迟** | 减少数据传输带来的延迟 |
| **API 成本** | 缓存 token 按优惠价计费 |

> ⚠️ **关于 TPM 限制**  
> 缓存的 token 仍计入 TPM（Tokens Per Minute）配额。如需控制 TPM，请使用请求节流或精简 Prompt。

### 工作原理

1. **增量更新**：自动检测对话历史，复用缓存前缀，仅发送新增消息（Delta）。
2. **智能键值**：自动规范化 System Prompt 中的时间戳，避免因时间变化导致缓存失效。
3. **安全退出**：程序退出时（Ctrl+C）自动清理所有缓存，防止持续计费。

### 调试日志

| 日志信息 | 含义 |
|---------|------|
| `[CACHE] 新缓存创建: xxx (含 N 条消息)` | 创建了包含历史消息的新缓存 |
| `[CACHE] 增量命中: xxx (缓存 N 条，增量 M 条)` | 复用缓存，仅发送 M 条新消息 |
| `[CACHE] 消息变化过大，重建缓存` | 历史消息不匹配，需重建缓存 |

### 注意事项

- 缓存创建耗时约 1-2 秒，但能显著减少后续请求延迟
- 如果 System Prompt 或 Tools 发生变化，会自动创建新缓存

## 🚦 TPM 速率限制

针对 `gemini-3-flash-preview` 等模型存在的 TPM (Tokens Per Minute) 限制，本工具内置了**令牌桶算法**进行平滑处理。

### 启用方式
使用 `--tpm` 参数指定速率上限，支持 `K/M` 后缀或纯数字：
```bash
./memobot-gemini-relay --tpm 0.9M     # 900,000 tokens/min
./memobot-gemini-relay --tpm 2000000  # 2,000,000 tokens/min
```

### 工作机制
1. **预估扣除**：请求发送前，根据 JSON Body 大小（字节/3）粗略估算 Token 数并扣除令牌。
2. **平滑等待**：如果令牌不足，程序会计算需等待秒数并自动阻塞（Sleep），之后再发送请求。
3. **精准修正**：收到 Gemini 响应后，根据 `usageMetadata.totalTokenCount` 进行修正（多退少补）。

> [!TIP]
> 推荐设置为模型 TPM 上限的 90% (如 1M 限制设为 `0.9M`)，以预留安全缓冲。
## 🖥️ 运行效果
启动后，你会看到如下提示：
```text
        用于 memU bot 的 Gemini API 中继工具
               memU bot 中配置如下：
---------------------------------------------------
        LLM 提供商：Custom Provider
        API 地址：http://127.0.0.1:6300/
        API 密钥：【Gemini api key】
        模型名称：gemini-3-flash-preview
---------------------------------------------------
[ ] --debug 显示处理状态
[ ] --cache 额外的缓存费用和减少的 token 费用
[ ] --proxy 代理，如 --proxy http://127.0.0.1:7890
[ ] --tpm 速率限制，如 --tpm 0.9M
---------------------------------------------------
当前正在中继Gemini api
```

## 许可证
MIT License
