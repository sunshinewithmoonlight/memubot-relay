//go:build gemini

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// --- 全局变量与标志 ---
var (
	debugMode bool
	cacheMode bool
	proxyURL  string
	tpmFlag   string                                             // 原始命令行输入，如 "0.9M" 或 "5000,000"
	apiKey    string = "AIzaSyD81zQQoHvwSVurzOOaWJtGI5ZiARySgwc" // 默认 Key

	// 签名缓存：tool_call_id -> thought_signature
	signatureCache   = make(map[string]string)
	signatureCacheMu sync.RWMutex

	// 上下文缓存：hash -> cache entry
	contextCache   = make(map[string]CacheEntry)
	contextCacheMu sync.RWMutex

	// 429 节流：收到 Resource Exhausted 后限制请求频率
	throttleMu      sync.Mutex
	throttleUntil   time.Time // 节流生效截止时间（30分钟后自动取消）
	throttleLastReq time.Time // 节流期间上次请求的时间
)

// --- 缓存管理 ---
type CacheEntry struct {
	Name         string // cachedContents/{id}
	ExpireAt     time.Time
	CachedCount  int    // 缓存的消息数量
	CachedDigest string // 缓存消息的摘要 (用于快速比对)
}

// 计算缓存键 (基于 System + Tools，忽略动态时间戳)
func computeCacheKey(system string, tools []geminiTool) string {
	// 规范化 system prompt，移除动态时间戳
	// 匹配: "Current date and time: 2026-02-09 (Monday) 21:15:02"
	normalizedSystem := normalizeSystemPrompt(system)

	h := sha256.New()
	h.Write([]byte(normalizedSystem))
	toolsJSON, _ := json.Marshal(tools)
	h.Write(toolsJSON)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// 规范化 system prompt，移除动态部分
func normalizeSystemPrompt(system string) string {
	// 移除时间戳: "Current date and time: YYYY-MM-DD (Day) HH:MM:SS"
	// 替换为固定字符串，保持结构一致
	import_regexp := regexp.MustCompile(`Current date and time: \d{4}-\d{2}-\d{2} \([^)]+\) \d{2}:\d{2}:\d{2}`)
	normalized := import_regexp.ReplaceAllString(system, "Current date and time: [NORMALIZED]")
	return normalized
}

// --- 缓存创建 API ---
type CreateCacheRequest struct {
	Model             string          `json:"model"`
	SystemInstruction *GoogleContent  `json:"systemInstruction,omitempty"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	Contents          []GoogleContent `json:"contents,omitempty"` // 对话历史
	TTL               string          `json:"ttl"`
}

type CreateCacheResponse struct {
	Name       string `json:"name"`       // cachedContents/{id}
	ExpireTime string `json:"expireTime"` // RFC 3339
}

func createCache(client *http.Client, apiKey, model string,
	systemInstruction *GoogleContent, tools []geminiTool) (string, error) {
	req := CreateCacheRequest{
		Model:             "models/" + model,
		SystemInstruction: systemInstruction,
		Tools:             tools,
		TTL:               "1800s", // 30 分钟
	}
	payload, _ := json.Marshal(req)

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/cachedContents?key=%s",
		apiKey)

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create cache failed: %s", string(body))
	}

	var result CreateCacheResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Name, nil
}

// 创建包含消息的缓存
func createCacheWithContents(client *http.Client, apiKey, model string,
	systemInstruction *GoogleContent, tools []geminiTool,
	contents []GoogleContent) (string, error) {
	req := CreateCacheRequest{
		Model:             "models/" + model,
		SystemInstruction: systemInstruction,
		Tools:             tools,
		Contents:          contents,
		TTL:               "1800s", // 30 分钟
	}
	payload, _ := json.Marshal(req)

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/cachedContents?key=%s",
		apiKey)

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create cache with contents failed: %s", string(body))
	}

	var result CreateCacheResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Name, nil
}

// 删除缓存
func deleteCache(client *http.Client, apiKey, cacheName string) error {
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/%s?key=%s",
		cacheName, apiKey)

	req, _ := http.NewRequest("DELETE", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if debugMode {
		fmt.Printf("[CACHE] 删除缓存: %s\n", cacheName)
	}
	return nil
}

// 计算消息摘要
func computeContentsDigest(contents []GoogleContent) string {
	contentsJSON, _ := json.Marshal(contents)
	hash := sha256.Sum256(contentsJSON)
	return hex.EncodeToString(hash[:])[:32]
}

// 检查当前消息是否是缓存消息的增量扩展
// 返回: (是否增量, 增量消息起始索引)
func isIncrementalUpdate(cachedDigest string, cachedCount int,
	currentContents []GoogleContent) (bool, int) {
	if cachedCount > len(currentContents) {
		// 当前消息比缓存少，说明是新对话
		return false, 0
	}

	// 计算当前消息中与缓存相同数量消息的摘要
	prefixContents := currentContents[:cachedCount]
	currentPrefixDigest := computeContentsDigest(prefixContents)

	if currentPrefixDigest == cachedDigest {
		// 前缀匹配，是增量更新
		return true, cachedCount
	}

	// 前缀不匹配，需要重建缓存
	return false, 0
}

// 保存缓存条目
func saveCacheEntry(key, name string, contents []GoogleContent) {
	digest := computeContentsDigest(contents)

	contextCacheMu.Lock()
	contextCache[key] = CacheEntry{
		Name:         name,
		ExpireAt:     time.Now().Add(25 * time.Minute),
		CachedCount:  len(contents),
		CachedDigest: digest,
	}
	contextCacheMu.Unlock()
}

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
	Contents          []GoogleContent   `json:"contents"`
	Tools             []geminiTool      `json:"tools,omitempty"`
	SystemInstruction *GoogleContent    `json:"systemInstruction,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
}

type GenerationConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

type GoogleResponse struct {
	Candidates []struct {
		Content struct {
			Parts []GooglePart `json:"parts"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
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
	flag.BoolVar(&cacheMode, "cache", false, "是否开启 Gemini 上下文缓存")
	flag.StringVar(&proxyURL, "proxy", "", "代理服务器地址 (如 http://127.0.0.1:7890)")
	flag.StringVar(&tpmFlag, "tpm", "", "TPM 速率限制 (如 0.9M 或 900,000)")
	flag.Parse()

	// 解析 TPM
	if tpmFlag != "" {
		tpmValue, err := parseTPM(tpmFlag)
		if err != nil {
			log.Fatalf("TPM 参数错误: %v", err)
		}
		tpmLimiter = NewTokenBucketLimiter(tpmValue)
	}

	fmt.Println("        用于 memU bot 的 Gemini API 中继工具")
	fmt.Println("               memU bot 中配置如下：")
	fmt.Println("---------------------------------------------------")
	fmt.Println("        LLM 提供商：Custom Provider")
	fmt.Println("        API 地址：http://127.0.0.1:6300/")
	fmt.Println("        API 密钥：【Gemini api key】")
	fmt.Println("        模型名称：gemini-3-flash-preview")
	fmt.Println("---------------------------------------------------")

	if !debugMode {
		fmt.Println("[ ] --debug 显示处理状态")
	} else {
		fmt.Println("[✓] --debug 显示处理状态")
	}

	if !cacheMode {
		fmt.Println("[ ] --cache 额外的缓存费用和减少的 token 费用")
	} else {
		fmt.Println("[✓] --cache 额外的缓存费用和减少的 token 费用")
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

	fmt.Println("---------------------------------------------------")
	fmt.Println("当前正在中继Gemini api")

	http.HandleFunc("/v1/", handleProxy)

	if cacheMode {
		// fmt.Println("按 Ctrl+C 退出并清理缓存")

		// 设置信号处理
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// 启动 HTTP 服务器
		server := &http.Server{Addr: ":6300"}

		go func() {
			if err := server.ListenAndServe(); err != http.ErrServerClosed {
				log.Fatalf("HTTP server error: %v", err)
			}
		}()

		// 等待中断信号
		<-ctx.Done()
		stop()
		fmt.Println("\n[EXIT] 正在关闭...")

		// 清理所有缓存
		cleanupCaches()

		// 关闭服务器
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
		fmt.Println("[EXIT] 完成")
	} else {
		log.Fatal(http.ListenAndServe(":6300", nil))
	}
}

// cleanupCaches 删除所有缓存避免继续计费
func cleanupCaches() {
	contextCacheMu.RLock()
	cacheCount := len(contextCache)
	contextCacheMu.RUnlock()

	if cacheCount == 0 {
		fmt.Println("[EXIT] 无缓存需要清理")
		return
	}

	fmt.Printf("[EXIT] 正在清理 %d 个缓存...\n", cacheCount)

	// 创建一个简单的 HTTP client
	transport := &http.Transport{}
	if proxyURL != "" {
		pURL, _ := url.Parse(proxyURL)
		transport.Proxy = http.ProxyURL(pURL)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	// 删除所有缓存
	contextCacheMu.RLock()
	for _, entry := range contextCache {
		if err := deleteCache(client, apiKey, entry.Name); err != nil {
			fmt.Printf("[EXIT] 删除缓存失败 %s: %v\n", entry.Name, err)
		} else {
			fmt.Printf("[EXIT] 已删除缓存: %s\n", entry.Name)
		}
	}
	contextCacheMu.RUnlock()
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
							// 始终使用 functionCall 格式（不再回退为文本）
							part := GooglePart{
								FunctionCall: &geminiFunctionCall{
									Name: block.Name,
									Args: args,
								},
							}
							if signature != "" {
								part.ThoughtSignature = signature
							} else {
								part.ThoughtSignature = "skip_thought_signature_validator"
							}
							parts = append(parts, part)
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
					ThoughtSignature: "skip_thought_signature_validator",
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
			// 如果上一条消息的角色与当前相同，则合并 parts（Gemini 不允许连续的相同角色对话）
			if len(gReq.Contents) > 0 && gReq.Contents[len(gReq.Contents)-1].Role == role {
				gReq.Contents[len(gReq.Contents)-1].Parts = append(gReq.Contents[len(gReq.Contents)-1].Parts, parts...)
			} else {
				gReq.Contents = append(gReq.Contents, GoogleContent{
					Role:  role,
					Parts: parts,
				})
			}
		}
	}
	// === 1.4.1 确保对话不以 model 开头（Gemini 要求 functionCall 之前必须有 user/functionResponse）===
	if len(gReq.Contents) > 0 && gReq.Contents[0].Role == "model" {
		gReq.Contents = append([]GoogleContent{{
			Role:  "user",
			Parts: []GooglePart{{Text: "continue"}},
		}}, gReq.Contents...)
		if debugMode {
			fmt.Println("[DEBUG] 对话以 model 开头，已插入占位 user 消息")
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

	// === 1.6 缓存处理（仅在 --cache 模式下启用）===
	if cacheMode {
		var cacheName string
		var deltaContents []GoogleContent

		if gReq.SystemInstruction != nil || len(gReq.Tools) > 0 {
			// 计算基础缓存键 (System + Tools)
			cacheKey := computeCacheKey(genReq.System, gReq.Tools)

			contextCacheMu.RLock()
			entry, exists := contextCache[cacheKey]
			contextCacheMu.RUnlock()

			if exists && time.Now().Before(entry.ExpireAt) {
				// 有缓存，检查消息是否增量
				isIncremental, startIdx := isIncrementalUpdate(
					entry.CachedDigest, entry.CachedCount, gReq.Contents)

				if isIncremental && startIdx < len(gReq.Contents) {
					// 增量更新：使用缓存，只发送新消息
					cacheName = entry.Name
					deltaContents = gReq.Contents[startIdx:]
					if debugMode {
						fmt.Printf("[CACHE] 增量命中: %s (缓存 %d 条，增量 %d 条)\n",
							cacheName, entry.CachedCount, len(deltaContents))
					}
				} else {
					// 非增量：删除旧缓存，创建新缓存
					if debugMode {
						fmt.Printf("[CACHE] 消息变化过大，重建缓存\n")
					}
					deleteCache(client, reqKey, entry.Name)

					// 缓存除最后一条外的所有消息（Gemini 要求 contents 非空）
					if len(gReq.Contents) > 1 {
						contentsToCache := gReq.Contents[:len(gReq.Contents)-1]
						name, err := createCacheWithContents(client, reqKey, genReq.Model,
							gReq.SystemInstruction, gReq.Tools, contentsToCache)
						if err != nil {
							fmt.Printf("[CACHE] 创建失败: %v\n", err)
						} else {
							cacheName = name
							deltaContents = gReq.Contents[len(gReq.Contents)-1:]
							saveCacheEntry(cacheKey, name, contentsToCache)
							if debugMode {
								fmt.Printf("[CACHE] 新缓存创建: %s (含 %d 条消息，增量 %d 条)\n",
									cacheName, len(contentsToCache), len(deltaContents))
							}
						}
					}
					// 如果只有 1 条消息，不创建缓存，直接发送完整请求
				}
			} else {
				// 无缓存或已过期，创建新缓存（缓存除最后一条外的所有消息）
				if len(gReq.Contents) > 1 {
					contentsToCache := gReq.Contents[:len(gReq.Contents)-1]
					name, err := createCacheWithContents(client, reqKey, genReq.Model,
						gReq.SystemInstruction, gReq.Tools, contentsToCache)
					if err != nil {
						fmt.Printf("[CACHE] 创建失败: %v\n", err)
					} else {
						cacheName = name
						deltaContents = gReq.Contents[len(gReq.Contents)-1:]
						saveCacheEntry(cacheKey, name, contentsToCache)
						if debugMode {
							fmt.Printf("[CACHE] 新缓存创建: %s (含 %d 条消息，增量 %d 条)\n",
								cacheName, len(contentsToCache), len(deltaContents))
						}
					}
				}
				// 如果只有 1 条消息，不创建缓存，直接发送完整请求
			}
		}

		// 设置请求
		if cacheName != "" && len(deltaContents) > 0 {
			gReq.CachedContent = cacheName
			gReq.SystemInstruction = nil
			gReq.Tools = nil
			gReq.Contents = deltaContents
		}
	}

	// === 1.7 TPM 速率限制 ===
	var estimatedTokens float64
	if tpmLimiter != nil {
		// 粗估：JSON payload 字节数 / 4 (英文) 或 / 2 (中文混合)
		// 使用 / 3 作为折中
		payloadSize := len(bodyBytes) // 原始请求大小
		estimatedTokens = float64(payloadSize) / 3.0

		for {
			allowed, waitTime := tpmLimiter.Consume(estimatedTokens)
			if allowed {
				if debugMode {
					fmt.Printf("[TPM] ✅ 允许请求，预估 %.0f tokens\n", estimatedTokens)
				}
				time.Sleep(1 * time.Second)
				break
			}
			if waitTime < 0 {
				fmt.Printf("[TPM] ❌ 单次请求 %.0f tokens 超过 TPM 上限\n", estimatedTokens)
				http.Error(w, "Request too large for TPM limit", 429)
				return
			}
			fmt.Printf("[TPM] ⏳ 令牌不足，等待 %.1f 秒...\n", waitTime)
			time.Sleep(time.Duration((waitTime+1)*1000) * time.Millisecond)
		}
		gReq.GenerationConfig = &GenerationConfig{MaxOutputTokens: 4000}
	}

	// === 2. 发送请求 ===
	// client 已在缓存处理阶段创建

	googleURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", genReq.Model, reqKey)
	payload, _ := json.Marshal(gReq)

	if debugMode {
		fmt.Printf("[DEBUG] %s 发送给 Gemini API 的数据 (Payload): %s\n", time.Now().Format("15:04:05"), genReq.Model)
		fmt.Printf("%s\n", string(payload))
	}

	// === 2.1 429 节流检查 ===
	throttleMu.Lock()
	if time.Now().Before(throttleUntil) {
		elapsed := time.Since(throttleLastReq)
		if elapsed < 61*time.Second {
			wait := 61*time.Second - elapsed
			throttleMu.Unlock()
			fmt.Printf("[429 Resource Exhausted] ⏳ 节流中，等待 %.0f 秒...\n", wait.Seconds())
			time.Sleep(wait)
			throttleMu.Lock()
		}
		throttleLastReq = time.Now()
	}
	throttleMu.Unlock()

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
		if resp.StatusCode == 429 {
			if strings.Contains(string(gBody), "Resource has been exhausted") {
				// 激活节流：30分钟内每分钟最多一次请求
				throttleMu.Lock()
				throttleUntil = time.Now().Add(30 * time.Minute)
				throttleLastReq = time.Now()
				throttleMu.Unlock()
				fmt.Println("[429] 🚫 Resource Exhausted，已启动节流（每分钟最多1次请求，30分钟后自动取消）")
			}
			if tpmLimiter != nil {
				// tpmLimiter.ConsumeExtra(estimatedTokens)
				// 此处普通429 error的等待61秒尚未经过测试
				time.Sleep(61 * time.Second)
			}
		}
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

	// === TPM 事后修正（仅在预估偏低时追加扣减，预估偏高不退还）===
	if tpmLimiter != nil && gResp.UsageMetadata.TotalTokenCount > 0 {
		actualTokens := float64(gResp.UsageMetadata.TotalTokenCount)
		if actualTokens > estimatedTokens {
			// 预估偏低，追加扣减
			extra := actualTokens - estimatedTokens
			tpmLimiter.ConsumeExtra(extra)
			if debugMode {
				fmt.Printf("[TPM] 修正: 预估 %.0f, 实际 %.0f, 追加扣 %.0f\n",
					estimatedTokens, actualTokens, extra)
			}
		} else if debugMode && estimatedTokens > actualTokens {
			fmt.Printf("[TPM] 预估 %.0f, 实际 %.0f (预估偏高，不修正)\n",
				estimatedTokens, actualTokens)
		}
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
				"input_tokens":  gResp.UsageMetadata.PromptTokenCount,
				"output_tokens": gResp.UsageMetadata.CandidatesTokenCount,
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
