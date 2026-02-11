# 减少 Gemini 429 错误

## Proposed Changes

### [MODIFY] [memubot-gemini-relay.go](file:///Users/shine/memubot-gemini-relay/memubot-gemini-relay.go)

#### 1. TPM 事后修正：预估偏高不退还

预估 token > 实际 token 时不再退还差额（保留安全缓冲），仅在预估偏低时追加扣减：

```go
if actualTokens > estimatedTokens {
    extra := actualTokens - estimatedTokens
    tpmLimiter.ConsumeExtra(extra)
} else if debugMode && estimatedTokens > actualTokens {
    fmt.Printf("[TPM] 预估 %.0f, 实际 %.0f (预估偏高，不修正)\n", ...)
}
```

#### 2. 429 Resource Exhausted 节流机制

新增全局变量 `throttleUntil`、`throttleLastReq`：

- **触发**：收到 429 + `"Resource has been exhausted"` 时，设 `throttleUntil = now + 30min`
- **生效**：每次请求前检查，若距上次请求 < 61 秒则等待补满
- **自动取消**：30 分钟后 `throttleUntil` 过期，恢复正常频率

```go
// 请求前检查
if time.Now().Before(throttleUntil) {
    if elapsed < 61*time.Second {
        wait := 61*time.Second - elapsed
        time.Sleep(wait)
    }
    throttleLastReq = time.Now()
}
```

#### 3. 429 时 TPM 桶扣减 + 等待 61 秒

收到 429 后，将预估 token 额外计入令牌桶，并等待 61 秒再转发错误给客户端：

```go
if tpmLimiter != nil {
    tpmLimiter.ConsumeExtra(estimatedTokens)
    time.Sleep(61 * time.Second)
}
```

#### 4. TPM 启用时额外 sleep 1 秒 + 限制输出 4000 tokens

TPM 限流通过后，强制 sleep 1 秒增加请求间隔，并设置 `maxOutputTokens: 4000`：

```go
time.Sleep(1 * time.Second)
gReq.GenerationConfig = &GenerationConfig{MaxOutputTokens: 4000}
```

#### 5. `GoogleRequest` 添加 `GenerationConfig` 字段

```diff
 type GoogleRequest struct {
+    GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
 }
+type GenerationConfig struct {
+    MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
+}
```

## Verification Plan

`--debug --tpm 0.9M` 运行后观察：
- payload 中含 `"generationConfig":{"maxOutputTokens":4000}`
- 请求间有 1 秒间隔
- 收到 429 Resource Exhausted 后，后续请求间隔 ≥ 61 秒
- 30 分钟后节流自动取消
