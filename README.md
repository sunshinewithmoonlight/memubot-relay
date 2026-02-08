# memobot-gemini-relay (memU bot 专用版)

这是一个高效、轻量的 Go 语言中继服务器，旨在让 **memU bot** 能够无缝使用 **Google Gemini API**。它通过将标准的 OpenAI 或 Anthropic (Claude) 请求协议转译为 Gemini 原生格式，解决了协议不兼容的问题。

## ✨ 特性

- **memU bot 深度适配**: 自动处理 memU bot 发出的 `/v1/messages` (Anthropic) 或 `/v1/chat/completions` (OpenAI) 请求。
- **协议转换**: 将各种 API 格式的消息流完整映射至 Gemini `generateContent` 接口。
- **🔧 Function Call 支持**: 完整支持 Anthropic/MiniMax 风格的工具调用（`tool_use`/`tool_result`）。
- **🧠 Thinking Mode**: 支持 Gemini 2.0 的思考模式，自动处理 `thought_signature`。
- **内置代理**: 支持 `--proxy` 参数，方便在中国大陆等网络环境下通过本地代理访问 Google 服务。
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


## 🖥️ 运行效果
启动后，你会看到如下提示：
```text
用于 memU bot 的 Gemini API 中继工具
memU bot 设置如下：
----------------------------------
 LLM 提供商：Custom Provider
 API 地址：http://127.0.0.1:6300/
 API 密钥：【Gemini api key】
 模型名称：gemini-3-flash-preview
----------------------------------
使用 --proxy 让请求通过代理转发
如 --proxy http://127.0.0.1:7890
当前正在中继Gemini api
```

## 许可证
MIT License
