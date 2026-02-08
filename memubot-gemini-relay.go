package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// --- 全局变量与标志 ---
var (
	debugMode bool
	proxyURL  string
	apiKey    string = "AIzaSyD81zQQoHvwSVurzOOaWJtGI5ZiARySgwc" // 默认 Key

	// 签名缓存：tool_call_id -> thought_signature
	signatureCache   = make(map[string]string)
	signatureCacheMu sync.RWMutex
)

// --- 结构体定义 (通用/OpenAI/Anthropic 输入) ---

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Signature string          `json:"signature,omitempty"` // Gemini thought_signature
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

// --- 结构体定义 (Google Gemini API) ---

type GooglePart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"` // Gemini 2.0 Thinking
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type GoogleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GooglePart `json:"parts"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type GoogleRequest struct {
	Contents          []GoogleContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	SystemInstruction *GoogleContent  `json:"systemInstruction,omitempty"`
}

type GoogleResponse struct {
	Candidates []struct {
		Content struct {
			Parts []GooglePart `json:"parts"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
	} `json:"candidates"`
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
	flag.Parse()

	fmt.Println("用于 memU bot 的 Gemini API 中继工具")
	fmt.Println("memU bot 设置如下：")
	fmt.Println("----------------------------------")
	fmt.Println(" LLM 提供商：Custom Provider")
	fmt.Println(" API 地址：http://127.0.0.1:6300/")
	fmt.Println(" API 密钥：【Gemini api key】")
	fmt.Println(" 模型名称：gemini-3-flash-preview")
	fmt.Printf(" 调试模式 (Debug): %v\n", debugMode) // Force print debug status
	fmt.Println("----------------------------------")
	if proxyURL != "" {
		fmt.Printf("已启用代理: %s\n", proxyURL)
	} else {
		fmt.Println("使用 --proxy 让请求通过代理转发")
		fmt.Println("如 --proxy http://127.0.0.1:7890")
	}
	fmt.Println("当前正在中继Gemini api")

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

	// === 1. 构建 Gemini Request ===
	var gReq GoogleRequest

	// System Instruction
	if genReq.System != "" {
		gReq.SystemInstruction = &GoogleContent{
			Parts: []GooglePart{{Text: genReq.System}},
		}
	}

	// Tools - 支持 OpenAI 风格和 Anthropic/MiniMax 风格
	if len(genReq.Tools) > 0 {
		var toolNames []string
		var funcs []geminiFunctionDeclaration
		for _, t := range genReq.Tools {
			var name, desc string
			var params json.RawMessage

			if t.Type == "function" && t.Function.Name != "" {
				// OpenAI 风格: {"type": "function", "function": {...}}
				name = t.Function.Name
				desc = t.Function.Description
				params = t.Function.Parameters
			} else if t.Name != "" {
				// Anthropic/MiniMax 风格: {"name": "...", "description": "...", "input_schema": {...}}
				name = t.Name
				desc = t.Description
				params = t.InputSchema
			}

			if name != "" {
				toolNames = append(toolNames, name)
				funcs = append(funcs, geminiFunctionDeclaration{
					Name:        name,
					Description: desc,
					Parameters:  params,
				})
			}
		}
		if debugMode {
			fmt.Printf("[DEBUG] 客户端定义工具: %v\n", toolNames)
		}
		if len(funcs) > 0 {
			gReq.Tools = []geminiTool{{FunctionDeclarations: funcs}}
		}
	}

	// Messages
	// 首先建立 tool_use_id 到函数名的映射
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

	for _, m := range genReq.Messages {
		role := "user"
		var parts []GooglePart

		switch m.Role {
		case "system":
			continue

		case "user":
			role = "user"
			// 尝试解析 content 为数组 (Anthropic/MiniMax 格式)
			var contentBlocks []ContentBlock
			if err := json.Unmarshal(m.Content, &contentBlocks); err == nil {
				for _, block := range contentBlocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							parts = append(parts, GooglePart{Text: block.Text})
						}
					case "tool_result":
						// tool_result 转换为 Gemini 的 functionResponse
						var responseData map[string]any
						// tool_result 的内容可以是字符串或 JSON
						if len(block.Content) > 0 {
							// 先尝试解析为字符串
							var contentStr string
							if err := json.Unmarshal(block.Content, &contentStr); err == nil {
								// 是字符串，尝试进一步解析为 JSON
								if err := json.Unmarshal([]byte(contentStr), &responseData); err != nil {
									responseData = map[string]any{"result": contentStr}
								}
							} else {
								// 直接是 JSON 对象
								if err := json.Unmarshal(block.Content, &responseData); err != nil {
									responseData = map[string]any{"result": string(block.Content)}
								}
							}
						} else {
							responseData = map[string]any{"result": "ok"}
						}
						// 从映射中查找函数名
						funcName := toolIdToName[block.ToolUseId]
						if funcName == "" {
							funcName = block.ToolUseId // 回退使用原始 ID
						}
						parts = append(parts, GooglePart{
							FunctionResponse: &geminiFunctionResponse{
								Name:     funcName,
								Response: responseData,
							},
						})
					}
				}
			} else {
				// 简单字符串格式
				text := extractText(m.Content)
				if text != "" {
					parts = append(parts, GooglePart{Text: text})
				}
			}

		case "assistant":
			role = "model"
			// 尝试解析 content 为数组 (Anthropic/MiniMax 格式)
			var contentBlocks []ContentBlock
			if err := json.Unmarshal(m.Content, &contentBlocks); err == nil {
				for _, block := range contentBlocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							parts = append(parts, GooglePart{Text: block.Text})
						}
					case "tool_use":
						// tool_use 转换为 Gemini 的 functionCall
						var args map[string]any
						if len(block.Input) > 0 {
							if err := json.Unmarshal(block.Input, &args); err != nil {
								args = make(map[string]any)
							}
						}
						if block.Name != "" {
							// 优先使用 block 中的签名，否则从缓存读取
							signature := block.Signature
							if signature == "" && block.ID != "" {
								signatureCacheMu.RLock()
								signature = signatureCache[block.ID]
								signatureCacheMu.RUnlock()
							}
							// 如果没有签名，回退为文本描述（避免 Gemini 报错）
							if signature == "" {
								argsJson, _ := json.Marshal(args)
								parts = append(parts, GooglePart{
									Text: fmt.Sprintf("[Called tool %s with args: %s]", block.Name, string(argsJson)),
								})
							} else {
								parts = append(parts, GooglePart{
									FunctionCall: &geminiFunctionCall{
										Name: block.Name,
										Args: args,
									},
									ThoughtSignature: signature,
								})
							}
						}
					}
				}
			} else {
				// 简单字符串格式
				text := extractText(m.Content)
				if text != "" {
					parts = append(parts, GooglePart{Text: text})
				}
			}
			// OpenAI 格式的 tool_calls
			for _, tc := range m.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = make(map[string]any)
				}
				parts = append(parts, GooglePart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
					},
				})
			}

		case "tool":
			// OpenAI 格式的 tool 消息
			role = "user"
			funcName := m.Name
			if funcName == "" {
				funcName = m.ToolCallID
			}
			contentStr := extractText(m.Content)
			var responseData map[string]any
			if err := json.Unmarshal([]byte(contentStr), &responseData); err != nil {
				responseData = map[string]any{"result": contentStr}
			}
			parts = append(parts, GooglePart{
				FunctionResponse: &geminiFunctionResponse{
					Name:     funcName,
					Response: responseData,
				},
			})
		}

		if len(parts) > 0 {
			gReq.Contents = append(gReq.Contents, GoogleContent{
				Role:  role,
				Parts: parts,
			})
		}
	}

	// === 2. 发送请求 ===
	transport := &http.Transport{}
	if proxyURL != "" {
		pURL, _ := url.Parse(proxyURL)
		transport.Proxy = http.ProxyURL(pURL)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   120 * time.Second,
	}

	googleURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", genReq.Model, reqKey)
	payload, _ := json.Marshal(gReq)

	if debugMode {
		fmt.Printf("[DEBUG] %s 发送给 Gemini API 的数据 (Payload): %s\n", time.Now().Format("15:04:05"), genReq.Model)
		fmt.Printf("%s\n", string(payload))
	}

	gReqObj, _ := http.NewRequest("POST", googleURL, bytes.NewBuffer(payload))
	gReqObj.Header.Set("Content-Type", "application/json")

	startTime := time.Now()
	resp, err := client.Do(gReqObj)
	if err != nil {
		fmt.Printf("[ERR] 网络连接失败: %v\n", err)
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	gBody, _ := io.ReadAll(resp.Body)
	if debugMode {
		fmt.Printf("[DEBUG] %s 从 Gemini API 取得的数据 (Raw Response):\n%s\n", time.Now().Format("15:04:05"), string(gBody))
	}

	if resp.StatusCode != 200 {
		fmt.Printf("[ERR] Google 报错 (状态码 %d): %s\n", resp.StatusCode, string(gBody))
		w.WriteHeader(resp.StatusCode)
		w.Write(gBody)
		return
	}

	// === 3. 处理响应 ===
	var gResp GoogleResponse
	if err := json.Unmarshal(gBody, &gResp); err != nil {
		fmt.Printf("[ERR] 解析 Google 响应失败: %v\n", err)
		http.Error(w, "Failed to parse Google response", 500)
		return
	}

	if len(gResp.Candidates) > 0 {
		candidate := gResp.Candidates[0]

		var thinkingText string
		var thinkingSignature string
		var textBuf strings.Builder
		var toolCalls []map[string]interface{}
		var toolCallCounter int

		// 遍历 Parts 收集内容
		for _, part := range candidate.Content.Parts {
			// Gemini 的 Thought 部分
			if part.Thought && part.Text != "" {
				thinkingText = part.Text
			}
			// Gemini 的 ThoughtSignature
			if part.ThoughtSignature != "" {
				thinkingSignature = part.ThoughtSignature
			}
			// 普通文本（非 Thought）
			if part.Text != "" && !part.Thought {
				textBuf.WriteString(part.Text)
			}
			// 函数调用
			if part.FunctionCall != nil {
				toolCallCounter++
				toolCallId := fmt.Sprintf("call_function_%d_%d", time.Now().Unix(), toolCallCounter)
				toolUseBlock := map[string]interface{}{
					"type":  "tool_use",
					"id":    toolCallId,
					"name":  part.FunctionCall.Name,
					"input": part.FunctionCall.Args,
				}
				// 保存签名到缓存，并包含在响应中
				if part.ThoughtSignature != "" {
					toolUseBlock["signature"] = part.ThoughtSignature
					// 同时缓存签名，以防客户端不保留
					signatureCacheMu.Lock()
					signatureCache[toolCallId] = part.ThoughtSignature
					signatureCacheMu.Unlock()
				}
				toolCalls = append(toolCalls, toolUseBlock)
			}
		}

		// Fallback for Malformed Function Call
		if candidate.FinishReason == "MALFORMED_FUNCTION_CALL" && candidate.FinishMessage != "" {
			name, args := parseMalformedFunctionCall(candidate.FinishMessage)
			if name != "" && args != nil {
				toolCallCounter++
				toolCalls = append(toolCalls, map[string]interface{}{
					"type":  "tool_use",
					"id":    fmt.Sprintf("call_function_%d_%d", time.Now().Unix(), toolCallCounter),
					"name":  name,
					"input": args,
				})
			} else {
				content := candidate.FinishMessage
				content = strings.TrimPrefix(content, "Malformed function call: ")
				if idx := strings.LastIndex(content, "})"); idx != -1 {
					content = content[idx+2:]
				} else if idx := strings.LastIndex(content, "}"); idx != -1 {
					content = content[idx+1:]
				}
				textBuf.WriteString(strings.TrimSpace(content))
			}
		}

		// 构建 MiniMax 格式的 content 数组
		var contentArr []interface{}

		// 1. thinking 块 (如果有思考内容)
		if thinkingText != "" {
			thinkingBlock := map[string]interface{}{
				"type":     "thinking",
				"thinking": thinkingText,
			}
			if thinkingSignature != "" {
				thinkingBlock["signature"] = thinkingSignature
			}
			contentArr = append(contentArr, thinkingBlock)
		}

		// 2. text 块 (如果有文本内容)
		if textBuf.Len() > 0 {
			contentArr = append(contentArr, map[string]interface{}{
				"type": "text",
				"text": textBuf.String(),
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
				"input_tokens":  0,
				"output_tokens": 0,
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
		// No candidates
		fmt.Printf("[ERR] Gemini returned no candidates. 原始响应: %s\n", string(gBody))
		http.Error(w, "Gemini returned no candidates", 500)
	}
}
