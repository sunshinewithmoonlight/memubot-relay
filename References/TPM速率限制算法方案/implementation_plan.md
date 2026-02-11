# 为 memubot-gemini-relay 添加 TPM 速率限制

基于 `TPM速率限制算法方案.md` 中的令牌桶算法，为 `memubot-gemini-relay.go` 添加可选的 TPM (Tokens Per Minute) 速率限制功能。

## 核心设计

- **令牌桶算法**：以 TPM 为桶容量，每秒恢复 `TPM/60` 个令牌
- **两阶段修正**：请求前基于 JSON body 大小粗估 token 数进行扣减；请求后根据 Gemini 返回的 `usageMetadata.totalTokenCount` 修正（退还多扣部分或追加扣减）
- **等待策略**：令牌不足时计算等待时间，阻塞等待后重试（适合此中继工具的单用户 / 低并发场景）
- **可选功能**：`--tpm 0` 或不传则不限流

## Proposed Changes

### 全局变量与标志

#### [MODIFY] [memubot-gemini-relay.go](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go)

**1) 新增全局变量 `tpmFlag`（string 类型）和 `tpmLimiter`**

在 [全局变量区](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go#L24-L38) 中新增：

```go
tpmFlag string // 原始命令行输入，如 "0.9M" 或 "5000,000"
```

---

**2) 新增 `TokenBucketLimiter` 结构体及方法**

在文件中缓存管理相关代码之后（约 L207 之后）添加一个新的代码段：

```go
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
```

新增全局限流器实例：

```go
var tpmLimiter *TokenBucketLimiter // nil 表示不限流
```

---

**3) 新增 `parseTPM` 函数**

解析用户输入的 TPM 值，支持以下格式：
- `0.9M` / `0.9m` → 900,000
- `1M` → 1,000,000
- `900000` → 900,000
- `900,000` → 900,000（忽略英文逗号）
- `5000,000` → 5,000,000

```go
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
```

---

**4) 修改 `main()` 函数**

在 [flag 定义区](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go#L428-L432) 添加：

```diff
 flag.BoolVar(&debugMode, "debug", false, "是否开启调试模式")
 flag.BoolVar(&cacheMode, "cache", false, "是否开启 Gemini 上下文缓存")
 flag.StringVar(&proxyURL, "proxy", "", "代理服务器地址")
+flag.StringVar(&tpmFlag, "tpm", "", "TPM 速率限制 (如 0.9M 或 900,000)")
 flag.Parse()
+
+// 解析 TPM
+if tpmFlag != "" {
+    tpmValue, err := parseTPM(tpmFlag)
+    if err != nil {
+        log.Fatalf("TPM 参数错误: %v", err)
+    }
+    tpmLimiter = NewTokenBucketLimiter(tpmValue)
+    fmt.Printf("[✓] --tpm %s (限制 %.0f tokens/min)\n", tpmFlag, tpmValue)
+} else {
+    fmt.Println("[ ] --tpm 速率限制，如 --tpm 0.9M")
+}
```

---

**5) 修改 `GoogleResponse` 结构体以解析 `usageMetadata`**

在 [GoogleResponse 定义](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go#L304-L312) 中添加 `UsageMetadata` 字段：

```diff
 type GoogleResponse struct {
     Candidates []struct {
         Content struct {
             Parts []GooglePart `json:"parts"`
         } `json:"content"`
         FinishReason  string `json:"finishReason"`
         FinishMessage string `json:"finishMessage"`
     } `json:"candidates"`
+    UsageMetadata struct {
+        TotalTokenCount int `json:"totalTokenCount"`
+    } `json:"usageMetadata"`
 }
```

---

**6) 在 `handleProxy` 中集成限流逻辑**

分两处修改：

**(a) 发送请求前（在 `=== 2. 发送请求 ===` 之前，约 [L877](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go#L877) ）：**

```go
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
            break
        }
        if waitTime < 0 {
            fmt.Printf("[TPM] ❌ 单次请求 %.0f tokens 超过 TPM 上限\n", estimatedTokens)
            http.Error(w, "Request too large for TPM limit", 429)
            return
        }
        fmt.Printf("[TPM] ⏳ 令牌不足，等待 %.1f 秒...\n", waitTime)
        time.Sleep(time.Duration(waitTime*1000) * time.Millisecond)
    }
}
```

**(b) 收到响应后修正（在成功解析 `gResp` 之后，构建 MiniMax 响应之前）：**

```go
// === TPM 事后修正 ===
if tpmLimiter != nil && gResp.UsageMetadata.TotalTokenCount > 0 {
    actualTokens := float64(gResp.UsageMetadata.TotalTokenCount)
    diff := estimatedTokens - actualTokens
    if diff > 0 {
        // 预估偏高，退还多扣的
        tpmLimiter.Refund(diff)
        if debugMode {
            fmt.Printf("[TPM] 修正: 预估 %.0f, 实际 %.0f, 退还 %.0f\n",
                estimatedTokens, actualTokens, diff)
        }
    } else if diff < 0 {
        // 预估偏低，追加扣减
        tpmLimiter.ConsumeExtra(-diff)
        if debugMode {
            fmt.Printf("[TPM] 修正: 预估 %.0f, 实际 %.0f, 追加扣 %.0f\n",
                estimatedTokens, actualTokens, -diff)
        }
    }
}
```

---

**7) 新增 import `"math"` 和 `"strconv"`**

在 [import 区](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go#L3-L22) 添加 `"math"` 和 `"strconv"`。

## Verification Plan

### 手动测试

1. **编译测试**：
   ```bash
   cd /Users/shine/memubot-gemini-relay && go build -o memubot-gemini-relay .
   ```

2. **参数解析测试** — 启动程序检查各种 `--tpm` 格式：
   ```bash
   ./memubot-gemini-relay --tpm 0.9M    # 应显示 900000
   ./memubot-gemini-relay --tpm 900,000  # 应显示 900000
   ./memubot-gemini-relay --tpm 5000000  # 应显示 5000000
   ./memubot-gemini-relay --tpm 5000,000 # 应显示 5000000
   ```
   每次启动后 Ctrl+C 退出即可，只需观察启动输出是否正确。

3. **限流集成测试** — 请用户用 memU bot 发送实际请求，观察带 `--debug --tpm` 的日志中 `[TPM]` 行，确认预估和修正逻辑正常。
