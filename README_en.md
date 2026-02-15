# memubot-relay (memU bot Special Edition)

This is a high-efficiency, lightweight Go language relay server designed to enable **memU bot** to seamlessly use the **Google Gemini API** or **OpenAI-Compatible Model API**. It solves protocol incompatibility issues by translating standard OpenAI or Anthropic (Claude) request protocols into the target API's native format.

This project includes two relay modes:
- **Gemini Relay** (`memubot-gemini-relay`) — Relay to Google Gemini API
- **OpenAI Relay** (`memubot-openai-relay`) — Relay to any OpenAI-compatible API (e.g., SiliconFlow, DeepSeek, etc.)

## ✨ Features

**Common Features:**
- **memU bot Deep Adaptation**: Automatically handles `/v1/messages` (Anthropic) requests sent by memU bot.
- **Protocol Conversion**: Maps message streams in various API formats completely to the target API native format, automatically merging consecutive same-role messages.
- **🔧 Function Call Support**: Fully supports Anthropic/MiniMax style tool calls (`tool_use`/`tool_result`).
- **Built-in Proxy**: Supports the `--proxy` parameter, facilitating access through a local proxy in network environments like mainland China.
- **TPM Rate Limiting**: Supports `--tpm` parameter (e.g., `0.9M`), using a token bucket algorithm to smooth request rates and prevent API rate limits.
- **Minimalist Operation**: No complex environment variable configuration required, ready to use upon startup.

**Gemini Relay Exclusive:**
- **🧠 Thinking Mode**: Supports Gemini 2.0's thinking mode, automatically handling `thought_signature`.
- **🔄 Turn Order Correction**: Automatically fixes conversation turn order issues that violate Gemini API requirements (e.g., conversations starting with `model` turn, consecutive same-role turns).
- **📦 Context Caching**: Enable via `--cache` parameter. Automatically caches System Prompt and Tools definitions, reducing network transfer and API costs.

**OpenAI Relay Exclusive:**
- **🔗 Flexible Endpoint**: Specify any OpenAI-compatible API endpoint via `--url` parameter.
- **🧠 Reasoning Content**: Automatically handles `reasoning_content` field (e.g., DeepSeek R1).

## ⚙️ memU bot Configuration Guide

In the settings interface of memU bot, please configure as shown below:

**When using Gemini Relay:**

| Configuration Item | Content |
| :--- | :--- |
| **LLM Provider** | `Custom Provider` |
| **API Address** | `http://127.0.0.1:6300/` |
| **API Key** | `Your Google Gemini API Key` |
| **Model Name** | `gemini-3-flash-preview` (or other Gemini models) |

**When using OpenAI Relay:**

| Configuration Item | Content |
| :--- | :--- |
| **LLM Provider** | `Custom Provider` |
| **API Address** | `http://127.0.0.1:6300/` |
| **API Key** | `OpenAI-Compatible Model API Key` |
| **Model Name** | `Pro/deepseek-ai/DeepSeek-V3.2` (or other OpenAI compatible models) |

## 🚀 Quick Start

### Gemini Relay
**Basic Run**:
```bash
./memubot-gemini-relay
```

For Windows, directly run `memubot-gemini-relay-windows.exe`.

**Run with Proxy**:
```bash
./memubot-gemini-relay --proxy http://127.0.0.1:7890
```

**Enable TPM Rate Limiting (Prevent 429 Errors)**:
```bash
./memubot-gemini-relay --tpm 0.9M  # Limit to 900k tokens/minute
```

**Enable Context Caching (reduce transfer and API costs)**:
```bash
./memubot-gemini-relay --cache
# Press Ctrl+C to gracefully exit and automatically clean up cache
```

**Debug Mode (View detailed packets)**:
```bash
./memubot-gemini-relay --debug
```

### OpenAI Relay
**Basic Run**:
```bash
chmod +x ./memubot-openai-relay
./memubot-openai-relay --url https://api.siliconflow.cn/v1/chat/completions
```

The `--url` is followed by the actual model endpoint address.

For Windows, directly run `memubot-openai-relay-windows.exe`.

**Run with Proxy**:
```bash
./memubot-openai-relay --url https://api.siliconflow.cn/v1/chat/completions --proxy http://127.0.0.1:7890
```

**Enable TPM Rate Limiting (Prevent 429 Errors)**:
```bash
./memubot-openai-relay --url https://api.siliconflow.cn/v1/chat/completions --tpm 0.9M
```

**Debug Mode (View detailed packets)**:
```bash
./memubot-openai-relay --url https://api.siliconflow.cn/v1/chat/completions --debug
```

### Run with Go Environment
```bash
go run -tags gemini .
go run -tags openai .
```

### Compilation
```bash
# Gemini relay
go mod init memubot-openai-relay && go build -tags gemini -o memubot-gemini-relay . && rm go.mod

# OpenAI relay
go mod init memubot-openai-relay && go build -tags openai -o memubot-openai-relay . && rm go.mod

# Cross-compile for Windows
go mod init memubot-openai-relay && GOOS=windows GOARCH=amd64 go build -tags gemini -o memubot-gemini-relay-windows.exe . && rm go.mod
go mod init memubot-openai-relay && GOOS=windows GOARCH=amd64 go build -tags openai -o memubot-openai-relay-windows.exe . && rm go.mod
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
2. **Thinking Mode**: Gemini Relay automatically caches and restores `thought_signature`; OpenAI Relay ignores returned `thinking` content.
3. **Debug Mode**: Use `--debug` to view complete request/response data.

## 📦 Context Caching

### Gemini Relay

> [!IMPORTANT]
> Context caching is **disabled** by default. When enabled, it may incur additional caching fees but will reduce token billing. Enable via the `--cache` parameter:
> ```bash
> ./memubot-gemini-relay --cache
> ```

This relay implements [Gemini Explicit Context Caching](https://ai.google.dev/gemini-api/docs/caching), automatically caching System Prompt and Tools definitions when enabled.

#### Benefits

| Dimension | Effect |
|-----------|--------|
| **Network Transfer** | Subsequent requests only send new messages, ~70% reduction |
| **Response Latency** | Reduced latency from less data transfer |
| **API Cost** | Cached tokens billed at discounted rate |

> ⚠️ **About TPM Limits**  
> Cached tokens still count toward TPM (Tokens Per Minute) quota. For TPM control, use request throttling or prompt optimization.

#### How It Works

1. **Incremental Update**: Automatically detects conversation history, reuses cache prefix, and sends only new messages (Delta).
2. **Smart Keying**: Automatically normalizes timestamps in System Prompt to prevent cache invalidation due to time changes.
3. **Safe Exit**: Automatically cleans up all caches upon program exit (Ctrl+C) to prevent continuous billing.

#### Debug Logs

| Log Message | Meaning |
|-------------|---------|
| `[CACHE] 新缓存创建: xxx (含 N 条消息)` | Created new cache containing historical messages |
| `[CACHE] 增量命中: xxx (缓存 N 条，增量 M 条)` | Reuse cache, sending only M new messages |
| `[CACHE] 消息变化过大，重建缓存` | History mismatch, rebuilding cache |

#### Notes

- Cache creation takes about 1-2 seconds but significantly reduces subsequent request latency
- If System Prompt or Tools change, a new cache is automatically created

### OpenAI Relay

OpenAI/DeepSeek has its own caching logic (e.g. automatic disk caching for prompts > 1024 tokens), which cannot be manually configured via this relay.

## 🚦 TPM Rate Limiting

To address TPM (Tokens Per Minute) limits on models, this tool includes a built-in **token bucket algorithm** for smooth rate limiting.

### How to Enable
Use the `--tpm` parameter to specify the rate limit, supporting `K/M` suffixes or raw numbers:
```bash
./memubot-gemini-relay --tpm 0.9M     # 900,000 tokens/min
./memubot-openai-relay --url ... --tpm 0.9M
```

### Mechanism

1. **Adaptive Estimation**: Before sending a request, tokens are estimated based on JSON Body size (bytes/3) multiplied by an adaptive ratio. The ratio auto-calibrates using exponential moving average of actual token counts from previous requests — it gets more accurate over time.
2. **Smooth Waiting**: If tokens are insufficient, the program calculates the wait time and automatically blocks (Sleeps) before sending the request.
3. **Bidirectional Correction**: Deducts extra tokens if estimated too low; refunds excess tokens if estimated too high, ensuring the token bucket accurately reflects actual usage.
4. **429 Smart Throttling**:
   - On standard 429 error: A 61-second cooldown is enforced.
   - On `"Resource has been exhausted"` error: Triggers a 30-minute throttling mode, forcing a 61-second interval between requests.
5. **Output Control**: When TPM is enabled, `maxOutputTokens` is capped at 4000.

> [!TIP]
> It is recommended to set this to 90% of the model's TPM limit (e.g., set `0.9M` for a 1M limit) to provide a safety buffer.

## 🖥️ Running Effect

**Gemini Relay** startup:
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

**OpenAI Relay** startup:
```text
     用于 memU bot 的 OpenAI-Compatible API 中继工具
               memU bot 中配置如下：
--------------------------------------------------------
        LLM 提供商：Custom Provider
        API 地址：http://127.0.0.1:6300/
        API 密钥：【OpenAI-Compatible api key】
        模型名称：【OpenAI-Compatible-reasoner】
--------------------------------------------------------
[ ] --debug 显示处理状态
[ ] --proxy 代理，如 --proxy http://127.0.0.1:7890
[ ] --tpm 速率限制，如 --tpm 0.9M
[✓] --url https://api.siliconflow.cn/v1/chat/completions
--------------------------------------------------------
当前正在中继 OpenAI-Compatible API
```

## License
MIT License
