这是一个针对 LLM（大语言模型）API 开发的 **TPM (Tokens Per Minute) 本地流控算法**方案。该方案采用业界主流的**令牌桶算法 (Token Bucket)**，能有效处理突发流量并确保请求平滑。

---

# TPM (Tokens Per Minute) 速率限制算法方案

### 1. 核心逻辑设计

对于 TPM 限制，简单的“单次检查”是不够的。我们需要引入**时间权重**。

*   **设定阈值**：建议设为官方限制的 90%（例如 1M 的限制，本地设为 900k），留出余量应对网络延迟和误差。
*   **计算公式**：
    *   **令牌恢复速率**：$Rate = \frac{TPM\_Limit}{60}$ (每秒生成的令牌数)。
    *   **当前可用令牌**：$Current = \min(Max\_TPM, \ Current + \Delta t \times Rate)$。
    *   **判断准则**：如果 $Current \ge 请求Token量(a)$，则允许请求并扣除；否则进入等待或拒绝。

---

### 2. Python 实现代码

该算法支持多线程安全，并能计算出精确的等待时间（Retry-After）。

```python
import time
import threading

class TokenBucketLimiter:
    def __init__(self, tpm_limit=900000):
        """
        :param tpm_limit: 每分钟允许的最大 Token 数量 (建议设为官方限制的 90%)
        """
        self.max_capacity = tpm_limit
        self.tokens_per_second = tpm_limit / 60.0
        self.current_tokens = tpm_limit  # 初始状态桶是满的
        self.last_update_time = time.time()
        self.lock = threading.Lock()

    def consume(self, token_count):
        """
        尝试消耗 token
        :param token_count: 本次请求预估消耗的 token (Prompt + Max_Tokens)
        :return: (is_allowed, wait_time)
        """
        with self.lock:
            # 1. 基础检查：单次请求是否超过总上限
            if token_count > self.max_capacity:
                return False, -1  # 永远不可能满足

            # 2. 根据时间流逝，恢复令牌
            now = time.time()
            elapsed = now - self.last_update_time
            self.current_tokens = min(
                self.max_capacity, 
                self.current_tokens + elapsed * self.tokens_per_second
            )
            self.last_update_time = now

            # 3. 核心判断
            if self.current_tokens >= token_count:
                # 令牌足够，允许通过
                self.current_tokens -= token_count
                return True, 0
            else:
                # 令牌不足，计算还需等待多久才能积累足够令牌
                needed_tokens = token_count - self.current_tokens
                wait_time = needed_tokens / self.tokens_per_second
                return False, wait_time

# --- 使用示例 ---

limiter = TokenBucketLimiter(900000)  # 900k TPM

def handle_request(prompt_tokens):
    # a = 预估请求消耗量 (Prompt + 预期的 Response Max Tokens)
    a = prompt_tokens + 500 
    
    allowed, detail = limiter.consume(a)
    
    if allowed:
        print(f"✅ [Allowed] 消耗 {a} tokens，立即发送请求")
        # 此处执行真正的 API 调用
    else:
        if detail == -1:
            print(f"❌ [Rejected] 请求量 {a} 超过了 TPM 总上限")
        else:
            print(f"⏳ [Rate Limited] 需等待 {detail:.2f} 秒后再试")
            # 策略：可以 time.sleep(detail) 自动重试，或返回给前端
```

---

### 3. 算法完善要点

针对你提到的“上次请求 $b$”和“这次请求 $a$”的关系，在完善后的逻辑中体现为：

1.  **动态窗口化**：不再只看上一次请求 $b$ 的具体数值，而是看上一次请求 $b$ **距离现在过去了多久**。
    *   如果 $b$ 刚发生，桶里令牌还没回满，这时 $a$ 可能会被限制。
    *   如果 $b$ 已经过去很久（比如 1 分钟），则 $b$ 的消耗完全恢复，$a$ 可以不受影响地发送。

2.  **两阶段修正（重要）**：
    *   **请求前**：使用 `tiktoken` 计算 Prompt 长度 + 设置的 `max_tokens` 作为预估值 $a$ 调用 `consume(a)`。
    *   **请求后**：API 返回后，会有实际消耗的 `total_tokens`。如果实际消耗远小于预估值，可以调用一个 `refund(amount)` 方法将多扣除的令牌退还给桶。

3.  **结合 RPM 限制**：
    *   通常官方同时有 RPM（Requests Per Minute）限制。建议在 `consume` 方法中增加一个 `self.requests_count` 的滑动窗口计数，确保单位时间内请求次数也不超标。

### 4. 推荐执行策略

| 场景 | 推荐策略 |
| :--- | :--- |
| **高并发后台任务** | `consume` 返回 `False` 时，线程进入 `time.sleep(wait_time)`，实现自动削峰填谷。 |
| **实时前端 API** | `consume` 返回 `False` 时，直接返回 `429 Too Many Requests`，并携带 `Retry-After` 响应头。 |
| **极端大请求** | 如果 $a$ 接近 `MAX_TPM`，建议强制切分长文本，否则会阻塞后续所有小请求。 |