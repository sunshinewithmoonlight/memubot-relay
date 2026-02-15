//go:build openai

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- 全局变量与标志 ---
var (
	debugMode bool
	proxyURL  string
	tpmFlag   string // 原始命令行输入，如 "0.9M" 或 "5000,000"
	apiKey    string // OpenAI-Compatible API Key (通过请求头传入)
	baseURL   string // 完整的 API 端点 URL (如 https://api.siliconflow.cn/v1/chat/completions)
)

// --- TPM 速率限制 ---

type TokenBucketLimiter struct {
	maxCapacity     float64
	tokensPerSecond float64
	currentTokens   float64
	lastUpdateTime  time.Time
	mu              sync.Mutex
}

func NewTokenBucketLimiter(tpmLimit float64) *TokenBucketLimiter {
	return &TokenBucketLimiter{
		maxCapacity:     tpmLimit,
		tokensPerSecond: tpmLimit / 60.0,
		currentTokens:   tpmLimit, // 初始满桶
		lastUpdateTime:  time.Now(),
	}
}

// Consume 尝试消耗 token。返回 (是否允许, 需等待秒数)
func (tb *TokenBucketLimiter) Consume(tokenCount float64) (bool, float64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tokenCount > tb.maxCapacity {
		return false, -1 // 超过总上限
	}

	// 回补令牌
	now := time.Now()
	elapsed := now.Sub(tb.lastUpdateTime).Seconds()
	tb.currentTokens = math.Min(tb.maxCapacity, tb.currentTokens+elapsed*tb.tokensPerSecond)
	tb.lastUpdateTime = now

	if tb.currentTokens >= tokenCount {
		tb.currentTokens -= tokenCount
		return true, 0
	}

	needed := tokenCount - tb.currentTokens
	waitTime := needed / tb.tokensPerSecond
	return false, waitTime
}

// Refund 退还多扣的令牌（事后修正）
func (tb *TokenBucketLimiter) Refund(amount float64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.currentTokens = math.Min(tb.maxCapacity, tb.currentTokens+amount)
}

// ConsumeExtra 追加扣减（实际用量 > 预估时）
func (tb *TokenBucketLimiter) ConsumeExtra(amount float64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.currentTokens -= amount
	// 允许变负，下次请求会等待
}

var tpmLimiter *TokenBucketLimiter // nil 表示不限流
var adaptiveRatio float64 = 1.0    // 自适应比率：actual / rawEstimate，初始 1.0
var adaptiveRatioMu sync.Mutex

func parseTPM(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "") // 忽略英文逗号

	if strings.HasSuffix(strings.ToUpper(s), "M") {
		numStr := s[:len(s)-1]
		val, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析 TPM 值: %s", s)
		}
		return val * 1_000_000, nil
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("无法解析 TPM 值: %s", s)
	}
	return val, nil
}

// --- 结构体定义 (通用/OpenAI/Anthropic 输入) ---

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Signature string          `json:"signature,omitempty"`
	// tool_result 字段
	ToolUseId string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type GenericMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type GenericTool struct {
	// OpenAI 风格
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
	// Anthropic/MiniMax 风格 (顶层字段)
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type GenericRequest struct {
	Model    string           `json:"model"`
	System   string           `json:"system,omitempty"`
	Messages []GenericMessage `json:"messages"`
	Tools    []GenericTool    `json:"tools,omitempty"`
}

// --- OpenAI API Request/Response Structs ---

type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content,omitempty"` // string or null
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type OpenAIToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
	Tools    []OpenAIToolDef `json:"tools,omitempty"`
}

type OpenAIResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Role             string           `json:"role"`
			Content          *string          `json:"content"`
			ReasoningContent *string          `json:"reasoning_content,omitempty"`
			ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// --- 辅助函数 ---

func extractText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var combined []string
		for _, b := range blocks {
			if b.Type == "text" {
				combined = append(combined, b.Text)
			}
		}
		return strings.Join(combined, "\n")
	}
	return string(raw)
}

// fixJSON 尝试修复非标准 JSON (如键未加引号)
func fixJSON(s string) string {
	var res strings.Builder
	var keyStart = -1
	inStr := false

	for i, r := range s {
		if r == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
		}

		if !inStr {
			// 简单的 Key 识别逻辑：字母数字下划线
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
				if keyStart == -1 {
					keyStart = i
				}
			} else {
				if keyStart != -1 {
					// 遇到非 key 字符，检查是否以 : 结尾表明是 key
					if r == ':' {
						// 这是一个 key，加上引号
						res.WriteRune('"')
						res.WriteString(s[keyStart:i])
						res.WriteRune('"')
						keyStart = -1
					} else if r == ' ' || r == '\t' || r == '\n' {
						// 空白字符忽略
					} else {
						// 不是 key (可能是 true/false/null/数字)
						res.WriteString(s[keyStart:i])
						keyStart = -1
					}
				}
				res.WriteRune(r)
			}
		} else {
			if keyStart != -1 {
				// 进入字符串了，之前的 keyStart 无效 (理论上不应发生)
				res.WriteString(s[keyStart:i])
				keyStart = -1
			}
			res.WriteRune(r)
		}
	}
	if keyStart != -1 {
		res.WriteString(s[keyStart:])
	}
	return res.String()
}

func parseMalformedFunctionCall(msg string) (string, map[string]any) {
	// 格式: Malformed function call: call:name{args}
	msg = strings.TrimPrefix(msg, "Malformed function call: ")
	msg = strings.TrimSpace(msg)

	if !strings.HasPrefix(msg, "call:") {
		return "", nil
	}

	startBrace := strings.Index(msg, "{")
	endBrace := strings.LastIndex(msg, "}")

	if startBrace == -1 || endBrace == -1 || endBrace < startBrace {
		return "", nil
	}

	// Extract Name
	// call:bash:ls_bash(   {...}
	namePart := msg[5:startBrace]
	namePart = strings.TrimRight(namePart, " (")
	name := strings.Trim(namePart, ": ")
	name = strings.ReplaceAll(name, ":", "_")

	// Extract Args
	argsRaw := msg[startBrace : endBrace+1]

	// Parse
	var args map[string]any
	// Try standard unmarshal first
	if err := json.Unmarshal([]byte(argsRaw), &args); err == nil {
		return name, args
	}

	// Try fixJSON
	fixedArgs := fixJSON(argsRaw)
	if err := json.Unmarshal([]byte(fixedArgs), &args); err == nil {
		return name, args
	}

	fmt.Printf("[WARN] 无法解析 Malformed Args: %s\n", argsRaw)
	return "", nil
}

func main() {
	flag.BoolVar(&debugMode, "debug", false, "是否开启调试模式")
	flag.StringVar(&proxyURL, "proxy", "", "代理服务器地址 (如 http://127.0.0.1:7890)")
	flag.StringVar(&tpmFlag, "tpm", "", "TPM 速率限制 (如 0.9M 或 900,000)")
	flag.StringVar(&baseURL, "url", "", "API 完整端点 URL (如 https://api.siliconflow.cn/v1/chat/completions)")
	flag.StringVar(&apiKey, "key", "", "API Key (也可通过请求头传入)")
	flag.Parse()

	// 验证必需参数
	if baseURL == "" {
		log.Fatal("必须指定 --url 参数，如 --url https://api.siliconflow.cn/v1/chat/completions")
	}
	// 移除末尾的斜杠
	baseURL = strings.TrimRight(baseURL, "/")

	// 解析 TPM
	if tpmFlag != "" {
		tpmValue, err := parseTPM(tpmFlag)
		if err != nil {
			log.Fatalf("TPM 参数错误: %v", err)
		}
		tpmLimiter = NewTokenBucketLimiter(tpmValue)
	}

	fmt.Println("     用于 memU bot 的 OpenAI-Compatible API 中继工具")
	fmt.Println("               memU bot 中配置如下：")
	fmt.Println("--------------------------------------------------------")
	fmt.Println("        LLM 提供商：Custom Provider")
	fmt.Println("        API 地址：http://127.0.0.1:6300/")
	fmt.Println("        API 密钥：【OpenAI-Compatible api key】")
	fmt.Println("        模型名称：【OpenAI-Compatible-reasoner】")
	fmt.Println("--------------------------------------------------------")

	if !debugMode {
		fmt.Println("[ ] --debug 显示处理状态")
	} else {
		fmt.Println("[✓] --debug 显示处理状态")
	}

	if proxyURL == "" {
		fmt.Println("[ ] --proxy 代理，如 --proxy http://127.0.0.1:7890")
	} else {
		fmt.Printf("[✓] --proxy %s 代理\n", proxyURL)
	}

	if tpmFlag != "" {
		tpmValue, _ := parseTPM(tpmFlag)
		fmt.Printf("[✓] --tpm %s (限制 %.0f tokens/min)\n", tpmFlag, tpmValue)
	} else {
		fmt.Println("[ ] --tpm 速率限制，如 --tpm 0.9M")
	}

	fmt.Printf("[✓] --url %s\n", baseURL)

	fmt.Println("--------------------------------------------------------")
	fmt.Println("当前正在中继 OpenAI-Compatible API")

	http.HandleFunc("/v1/", handleProxy)
	log.Fatal(http.ListenAndServe(":6300", nil))
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	reqKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if reqKey == "" {
		reqKey = r.Header.Get("x-api-key")
	}
	if reqKey == "" {
		reqKey = apiKey
	}
	if reqKey == "" {
		fmt.Println("[ERR] 未提供 API Key (通过请求头传入)")
		http.Error(w, "Missing API Key", 401)
		return
	}

	bodyBytes, _ := io.ReadAll(r.Body)
	var genReq GenericRequest
	if err := json.Unmarshal(bodyBytes, &genReq); err != nil {
		fmt.Printf("[ERR] JSON 解析失败: %v\n", err)
		http.Error(w, "Invalid JSON", 400)
		return
	}

	if debugMode {
		fmt.Printf("[DEBUG] %s 收到请求: %s %s | 模型: %s\n", time.Now().Format("15:04:05"), r.Method, path, genReq.Model)
		fmt.Printf("[DEBUG] %s 收到的数据 (Client Request):\n%s\n", time.Now().Format("15:04:05"), string(bodyBytes))
	}

	// === 1. 构建 OpenAI Request ===
	var oaiReq OpenAIRequest
	oaiReq.Model = genReq.Model

	// System message → 第一条消息
	if genReq.System != "" {
		oaiReq.Messages = append(oaiReq.Messages, OpenAIMessage{
			Role:    "system",
			Content: genReq.System,
		})
	}

	// Tools - 支持 OpenAI 风格和 Anthropic/MiniMax 风格
	if len(genReq.Tools) > 0 {
		var toolNames []string
		for _, t := range genReq.Tools {
			var td OpenAIToolDef
			td.Type = "function"

			if t.Type == "function" && t.Function.Name != "" {
				// OpenAI 风格: {"type": "function", "function": {...}}
				td.Function.Name = t.Function.Name
				td.Function.Description = t.Function.Description
				td.Function.Parameters = t.Function.Parameters
			} else if t.Name != "" {
				// Anthropic/MiniMax 风格: {"name": "...", "description": "...", "input_schema": {...}}
				td.Function.Name = t.Name
				td.Function.Description = t.Description
				td.Function.Parameters = t.InputSchema
			} else {
				continue
			}

			toolNames = append(toolNames, td.Function.Name)
			oaiReq.Tools = append(oaiReq.Tools, td)
		}
		if debugMode {
			fmt.Printf("[DEBUG] 客户端定义工具: %v\n", toolNames)
		}
	}

	// Messages - 建立 tool_use_id 到函数名的映射 (用于 Anthropic tool_result)
	toolIdToName := make(map[string]string)
	for _, m := range genReq.Messages {
		if m.Role == "assistant" {
			// 检查 Anthropic/MiniMax 格式的 content 数组
			var contentBlocks []ContentBlock
			if err := json.Unmarshal(m.Content, &contentBlocks); err == nil {
				for _, block := range contentBlocks {
					if block.Type == "tool_use" && block.ID != "" && block.Name != "" {
						toolIdToName[block.ID] = block.Name
					}
				}
			}
			// 检查 OpenAI 格式的 tool_calls
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					toolIdToName[tc.ID] = tc.Function.Name
				}
			}
		}
	}

	// 转换消息
	for _, m := range genReq.Messages {
		switch m.Role {
		case "system":
			continue // 系统消息已经在上面处理

		case "user":
			// 尝试解析 content 为数组 (Anthropic/MiniMax 格式)
			var contentBlocks []ContentBlock
			if err := json.Unmarshal(m.Content, &contentBlocks); err == nil {
				// 分离: text 内容 → user 消息, tool_result → tool 消息
				var textParts []string
				var toolResults []ContentBlock

				for _, block := range contentBlocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							textParts = append(textParts, block.Text)
						}
					case "tool_result":
						toolResults = append(toolResults, block)
					}
				}

				// 先添加 tool results (作为 OpenAI tool 消息)
				for _, tr := range toolResults {
					var contentStr string
					if len(tr.Content) > 0 {
						// 先尝试解析为字符串
						var s string
						if err := json.Unmarshal(tr.Content, &s); err == nil {
							contentStr = s
						} else {
							// 直接用 JSON 文本
							contentStr = string(tr.Content)
						}
					} else {
						contentStr = "ok"
					}
					oaiReq.Messages = append(oaiReq.Messages, OpenAIMessage{
						Role:       "tool",
						Content:    contentStr,
						ToolCallID: tr.ToolUseId,
						Name:       toolIdToName[tr.ToolUseId],
					})
				}

				// 再添加 text 内容 (如果有)
				if len(textParts) > 0 {
					oaiReq.Messages = append(oaiReq.Messages, OpenAIMessage{
						Role:    "user",
						Content: strings.Join(textParts, "\n"),
					})
				}
			} else {
				// 简单字符串格式
				text := extractText(m.Content)
				if text != "" {
					oaiReq.Messages = append(oaiReq.Messages, OpenAIMessage{
						Role:    "user",
						Content: text,
					})
				}
			}

		case "assistant":
			msg := OpenAIMessage{
				Role: "assistant",
			}

			// 尝试解析 content 为数组 (Anthropic/MiniMax 格式)
			var contentBlocks []ContentBlock
			if err := json.Unmarshal(m.Content, &contentBlocks); err == nil {
				var textParts []string
				var toolCalls []OpenAIToolCall

				for _, block := range contentBlocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							textParts = append(textParts, block.Text)
						}
					case "thinking":
						// OpenAI-Compatible thinking — 忽略回传 (由 OpenAI-Compatible 自行生成)
						continue
					case "tool_use":
						// tool_use → OpenAI tool_calls
						argsStr := "{}"
						if len(block.Input) > 0 {
							argsStr = string(block.Input)
						}
						tc := OpenAIToolCall{
							ID:   block.ID,
							Type: "function",
						}
						tc.Function.Name = block.Name
						tc.Function.Arguments = argsStr
						toolCalls = append(toolCalls, tc)
					}
				}

				if len(textParts) > 0 {
					combined := strings.Join(textParts, "\n")
					msg.Content = combined
				}
				if len(toolCalls) > 0 {
					msg.ToolCalls = toolCalls
				}
			} else {
				// 简单字符串格式
				text := extractText(m.Content)
				if text != "" {
					msg.Content = text
				}
			}
			// OpenAI 格式的 tool_calls (直接透传)
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, OpenAIToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}

			oaiReq.Messages = append(oaiReq.Messages, msg)

		case "tool":
			// OpenAI 格式的 tool 消息 — 直接透传
			contentStr := extractText(m.Content)
			oaiReq.Messages = append(oaiReq.Messages, OpenAIMessage{
				Role:       "tool",
				Content:    contentStr,
				ToolCallID: m.ToolCallID,
				Name:       m.Name,
			})
		}
	}

	// === 1.5 HTTP Client ===
	transport := &http.Transport{}
	if proxyURL != "" {
		pURL, _ := url.Parse(proxyURL)
		transport.Proxy = http.ProxyURL(pURL)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   120 * time.Second,
	}

	// === 1.7 TPM 速率限制 ===
	var estimatedTokens float64
	var rawEstimate float64
	if tpmLimiter != nil {
		// 粗估：JSON payload 字节数 / 3，再乘以自适应比率修正
		payloadSize := len(bodyBytes)
		rawEstimate = float64(payloadSize) / 3.0
		adaptiveRatioMu.Lock()
		estimatedTokens = rawEstimate * adaptiveRatio
		adaptiveRatioMu.Unlock()

		for {
			allowed, waitTime := tpmLimiter.Consume(estimatedTokens)
			if allowed {
				if debugMode {
					fmt.Printf("[TPM] ✅ 允许请求，预估 %.0f tokens\n", estimatedTokens)
				}
				// time.Sleep(1 * time.Second)
				break
			}
			if waitTime < 0 {
				fmt.Printf("[TPM] ❌ 单次请求 %.0f tokens 超过 TPM 上限\n", estimatedTokens)
				http.Error(w, "Request too large for TPM limit", 429)
				return
			}
			fmt.Printf("[TPM] ⏳ 令牌不足，等待 %.1f 秒...\n", waitTime)
			// time.Sleep(time.Duration((waitTime+1)*1000) * time.Millisecond)
			time.Sleep(time.Duration(waitTime*1000) * time.Millisecond)
		}
	}

	// === 2. 发送请求 ===
	targetURL := baseURL
	payload, _ := json.Marshal(oaiReq)

	if debugMode {
		fmt.Printf("[DEBUG] %s POST %s (模型: %s)\n", time.Now().Format("15:04:05"), targetURL, genReq.Model)
		fmt.Printf("[DEBUG] Payload:\n%s\n", string(payload))
	}

	httpReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(payload))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+reqKey)

	startTime := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Printf("[ERR] 网络连接失败: %v\n", err)
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if debugMode {
		fmt.Printf("[DEBUG] %s 从 OpenAI-Compatible API 取得的数据 (Raw Response):\n%s\n", time.Now().Format("15:04:05"), string(respBody))
	}

	if resp.StatusCode != 200 {
		fmt.Printf("[ERR] OpenAI-Compatible 报错 (状态码 %d): %s\n", resp.StatusCode, string(respBody))
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// === 3. 处理响应 ===
	var oaiResp OpenAIResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		fmt.Printf("[ERR] 解析 OpenAI-Compatible 响应失败: %v\n", err)
		http.Error(w, "Failed to parse OpenAI-Compatible response", 500)
		return
	}

	// === TPM 事后修正（双向修正 + 自适应比率更新）===
	if tpmLimiter != nil && oaiResp.Usage.TotalTokens > 0 {
		actualTokens := float64(oaiResp.Usage.TotalTokens)
		if actualTokens > estimatedTokens {
			// 预估偏低，追加扣减
			extra := actualTokens - estimatedTokens
			tpmLimiter.ConsumeExtra(extra)
			if debugMode {
				fmt.Printf("[TPM] 修正: 预估 %.0f, 实际 %.0f, 追加扣 %.0f\n",
					estimatedTokens, actualTokens, extra)
			}
		} else if estimatedTokens > actualTokens {
			// 预估偏高，退还多扣的令牌
			refund := estimatedTokens - actualTokens
			tpmLimiter.Refund(refund)
			if debugMode {
				fmt.Printf("[TPM] 修正: 预估 %.0f, 实际 %.0f, 退还 %.0f\n",
					estimatedTokens, actualTokens, refund)
			}
		}

		// 更新自适应比率 (指数移动平均: 80% 旧值 + 20% 新值)
		if rawEstimate > 0 {
			newRatio := actualTokens / rawEstimate
			adaptiveRatioMu.Lock()
			adaptiveRatio = 0.8*adaptiveRatio + 0.2*newRatio
			if debugMode {
				fmt.Printf("[TPM] 自适应比率更新: %.4f\n", adaptiveRatio)
			}
			adaptiveRatioMu.Unlock()
		}
	}

	if len(oaiResp.Choices) > 0 {
		choice := oaiResp.Choices[0]

		var thinkingText string
		var textContent string
		var toolCalls []map[string]interface{}
		var toolCallCounter int

		// OpenAI-Compatible R1 的推理内容
		if choice.Message.ReasoningContent != nil && *choice.Message.ReasoningContent != "" {
			thinkingText = *choice.Message.ReasoningContent
		}

		// 文本内容
		if choice.Message.Content != nil && *choice.Message.Content != "" {
			textContent = *choice.Message.Content
		}

		// 函数调用
		for _, tc := range choice.Message.ToolCalls {
			toolCallCounter++
			toolUseBlock := map[string]interface{}{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": json.RawMessage(tc.Function.Arguments),
			}
			toolCalls = append(toolCalls, toolUseBlock)
		}

		// 构建 MiniMax 格式的 content 数组
		var contentArr []interface{}

		// 1. thinking 块 (如果有推理内容)
		if thinkingText != "" {
			contentArr = append(contentArr, map[string]interface{}{
				"type":     "thinking",
				"thinking": thinkingText,
			})
		}

		// 2. text 块 (如果有文本内容)
		if textContent != "" {
			contentArr = append(contentArr, map[string]interface{}{
				"type": "text",
				"text": textContent,
			})
		}

		// 3. tool_use 块 (如果有函数调用)
		for _, tc := range toolCalls {
			contentArr = append(contentArr, tc)
		}

		// 确定 stop_reason
		stopReason := "end_turn"
		if len(toolCalls) > 0 {
			stopReason = "tool_use"
		}

		// 构建 MiniMax 格式响应
		res := map[string]interface{}{
			"id":          fmt.Sprintf("%x", time.Now().UnixNano()),
			"type":        "message",
			"role":        "assistant",
			"model":       genReq.Model,
			"content":     contentArr,
			"stop_reason": stopReason,
			"usage": map[string]interface{}{
				"input_tokens":  oaiResp.Usage.PromptTokens,
				"output_tokens": oaiResp.Usage.CompletionTokens,
			},
			"base_resp": map[string]interface{}{
				"status_code": 0,
				"status_msg":  "",
			},
		}

		if debugMode {
			respBytes, _ := json.MarshalIndent(res, "", "  ")
			fmt.Printf("[DEBUG] %s 成功响应 | 耗时: %v\n", time.Now().Format("15:04:05"), time.Since(startTime))
			fmt.Printf("[DEBUG] %s 发送回 memubot 的数据 (Response):\n%s\n", time.Now().Format("15:04:05"), string(respBytes))
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(res)
	} else {
		// No choices
		fmt.Printf("[ERR] OpenAI-Compatible returned no choices. 原始响应: %s\n", string(respBody))
		http.Error(w, "OpenAI-Compatible returned no choices", 500)
	}
}
