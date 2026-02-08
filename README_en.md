# memobot-gemini-relay (memU bot Special Edition)

This is a high-efficiency, lightweight Go language relay server designed to enable **memU bot** to seamlessly use the **Google Gemini API**. It solves protocol incompatibility issues by translating standard OpenAI or Anthropic (Claude) request protocols into the Gemini native format.

## ✨ Features

- **memU bot Deep Adaptation**: Automatically handles `/v1/messages` (Anthropic) or `/v1/chat/completions` (OpenAI) requests sent by memU bot.
- **Protocol Conversion**: Maps message streams in various API formats completely to the Gemini `generateContent` interface.
- **🔧 Function Call Support**: Fully supports Anthropic/MiniMax style tool calls (`tool_use`/`tool_result`).
- **🧠 Thinking Mode**: Supports Gemini 2.0's thinking mode, automatically handling `thought_signature`.
- **Built-in Proxy**: Supports the `--proxy` parameter, facilitating access to Google services through a local proxy in network environments like mainland China.
- **Minimalist Operation**: No complex environment variable configuration required, ready to use upon startup.

## ⚙️ memU bot Configuration Guide

In the settings interface of memU bot, please clear configuration as shown below:

| Configuration Item | Content |
| :--- | :--- |
| **LLM Provider** | `Custom Provider` |
| **API Address** | `http://127.0.0.1:6300/v1` |
| **API Key** | `Your Google Gemini API Key` |
| **Model Name** | `gemini-3-flash-preview` (or other Gemini models) |

## 🚀 Quick Start

### Running
**Basic Run**:
```bash
./memobot-gemini-relay
```

For Windows, directly run `memubot-gemini-relay-windows.exe`.

**Run with Proxy**:
```bash
./memobot-gemini-relay --proxy http://127.0.0.1:7890
```

**Debug Mode (View detailed packets)**:
```bash
./memobot-gemini-relay --debug
```

### Run with Go Environment
```bash
go run memubot-gemini-relay.go
```

### Compilation
```bash
go mod init memubot-gemini-relay && go build -o memubot-gemini-relay . && rm go.mod
GOOS=windows GOARCH=amd64 go build -o memubot-gemini-relay-windows.exe memubot-gemini-relay.go
```

## 🔧 Function Call Support

Supports Anthropic/MiniMax style tool definitions:
```json
{"name": "bash", "description": "...", "input_schema": {...}}
```

### Available Tools List (19 items)

| # | Tool Name | Description |
|---|---------|------|
| 1 | `bash` | Execute bash commands |
| 2 | `str_replace_editor` | File view/edit (view/create/str_replace/insert) |
| 3 | `download_file` | Download file from URL |
| 4 | `web_search` | Web search (Tavily AI) |
| 5 | `macos_launch_app` | Launch macOS application |
| 6 | `macos_mail` | Apple Mail operations |
| 7 | `macos_calendar` | Apple Calendar operations |
| 8 | `macos_contacts` | Apple Contacts query |
| 9 | `feishu_send_text` | Send Feishu text message |
| 10 | `feishu_send_image` | Send Feishu image |
| 11 | `feishu_send_file` | Send Feishu file |
| 12 | `feishu_send_card` | Send Feishu message card |
| 13 | `feishu_delete_chat_history` | Delete Feishu chat history |
| 14 | `service_create` | Create background service |
| 15 | `service_list` | List all services |
| 16 | `service_start` | Start service |
| 17 | `service_stop` | Stop service |
| 18 | `service_delete` | Delete service |
| 19 | `service_get_info` | Get service information |

### Test Prompt

| Test Tool | Prompt |
| :--- | :--- |
| `bash` | `Check what files are on my desktop` |
| `str_replace_editor` | `Help me create a test.txt file on the desktop with content "Hello!"` |
| `web_search` | `Search for today's weather` |
| `download_file` | `Download this image and save to desktop: https://example.com/image.png` |
| `macos_launch_app` | `Open Calendar app` |
| `macos_contacts` | `Search contacts for someone named "Zhang San"` |
| `macos_mail` | `Check how many unread emails I have` |
| `feishu_send_text` | `Send me a message saying "Test successful!"` |
| `feishu_send_card` | `Send a green message card with title "Test Report"` |
| `service_list` | `List what background services I have running now` |
| Combined Test | `Check the content of test.txt on the desktop, then send it to me via Feishu` |

### Notes

1. **New Conversation Start Test**: It is recommended to clear the conversation history and restart to ensure `thought_signature` is passed correctly.
2. **Thinking Mode**: Gemini 2.0 function calling requires `thought_signature`, which this relay automatically caches and restores.
3. **Debug Mode**: Use `--debug` to view complete request/response data.


## 🖥️ Running Effect
After startup, you will see the following prompt:
```text
Gemini API relay tool for memU bot
memU bot settings are as follows:
----------------------------------
 LLM Provider: Custom Provider
 API Address: http://127.0.0.1:6300/
 API Key: [Gemini api key]
 Model Name: gemini-3-flash-preview
----------------------------------
Use --proxy to forward requests through a proxy
e.g., --proxy http://127.0.0.1:7890
Currently relaying Gemini api
```

## License
MIT License
