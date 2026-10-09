package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 本文件是 CodeBuddy / WorkBuddy 出站 chat 请求体的兼容与省钱改写，移植自参考实现
// workbuddy2api-panel internal/upstream 的 payload.go / thinking.go / tool_pairing.go /
// cache_key.go。每一条都对应上游一个实测会 400 或白白多花积分的形态：
//
//   - max_completion_tokens → max_tokens：上游只认 max_tokens，别名被忽略后回落默认上限，长输出被截。
//   - GPT 系 max_tokens < 16 抬到 16：否则 400 code=11133（Claude Code 切模型时会发极小的探针）。
//   - stream_options.include_usage：流式请求补上，上游据此在末帧返回用量。
//   - tools 里 pattern 的 `\_` → `_`：上游 schema 校验拒收，400 code=11129。
//   - image_url 字符串 → {"url": ...}：上游只认对象形态，400 code=11101。
//   - tool_calls 与 tool 结果配对修复：背靠背的 assistant.tool_calls 合并、插在结果中间的消息后移、
//     孤儿调用/结果剔除；不修会 400 code=11148 并让整条会话报废。
//   - DeepSeek 思维链：注入 thinking.type=enabled 并补默认档位，否则上游不返回思维链；
//     多轮时 assistant 必须带 reasoning_content。
//   - reasoning_effort 降级到模型支持的档位：传不支持的档位会 400。
//   - prompt_cache_key：按账号隔离的稳定缓存键，同一前缀命中缓存后积分约降 17 倍。
//
// 都是"让请求通过"的协议兼容，不改语义；body 无法解析时原样返回。

// tencentCodeBuddyGPTMinMaxTokens 是 GPT 系上游接受的 max_tokens 下限。
const tencentCodeBuddyGPTMinMaxTokens = 16

// tencentCodeBuddyDefaultDeepSeekEffort 是官方客户端在模型未声明默认档时的兜底档位。
const tencentCodeBuddyDefaultDeepSeekEffort = "high"

// tencentCodeBuddyEffortRank 是推理档位从低到高的顺序。
var tencentCodeBuddyEffortRank = map[string]int{"off": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}

// prepareTencentCodeBuddyChatPayload 对出站 chat 请求体做兼容与省钱改写。
// conversationID 是从客户端请求里解析出的会话标识（可空），只用于生成 prompt_cache_key。
func prepareTencentCodeBuddyChatPayload(body []byte, account *Account, conversationID string, policy tencentCodeBuddyContentPolicy) []byte {
	if len(body) == 0 {
		return body
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var obj map[string]any
	if err := decoder.Decode(&obj); err != nil || obj == nil {
		return body
	}

	// 先替换/追加 system 提示词：后面的配对修复与回填都基于最终的消息列表。
	applyTencentCodeBuddySystemPromptPolicy(obj, policy)

	model, _ := obj["model"].(string)
	capability := TencentCodeBuddyModelCapability{}
	if account != nil {
		capability, _ = account.TencentCodeBuddyModelCapability(model)
	}

	translateTencentCodeBuddyMaxCompletionTokens(obj)
	clampTencentCodeBuddyGPTMinMaxTokens(obj)
	if stream, _ := obj["stream"].(bool); stream {
		if _, has := obj["stream_options"]; !has {
			obj["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	normalizeTencentCodeBuddyToolPatterns(obj)
	normalizeTencentCodeBuddyImageURL(obj)
	if messages, ok := obj["messages"].([]any); ok {
		// 顺序不能换：先合并背靠背的调用声明，repack 才看得到完整的一批调用；
		// 再把插在结果中间的消息后移，最后剔除仍然配不上的孤儿。
		messages, _ = mergeTencentCodeBuddyAdjacentToolCalls(messages)
		messages, _ = repackTencentCodeBuddyToolResultBlocks(messages)
		messages, _ = cleanupTencentCodeBuddyOrphanToolCalls(messages)
		obj["messages"] = messages
	}
	// 先补默认档再降级：补入的默认档模型不支持时也会落到支持的档位。
	injectTencentCodeBuddyThinking(obj, capability.DefaultEffort)
	normalizeTencentCodeBuddyReasoningEffort(obj, capability.SupportedEfforts)
	backfillTencentCodeBuddyReasoningContent(obj)
	// 脱敏放最后：回填镜像出来的 reasoning 字段也要洗到。
	if policy.Sanitize {
		if messages, ok := obj["messages"].([]any); ok {
			sanitizeTencentCodeBuddyMessages(messages)
		}
	}
	if account != nil {
		injectTencentCodeBuddyPromptCacheKey(obj, account.TencentCodeBuddyCredential().UserID, conversationID)
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(obj); err != nil {
		return body
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// tencentCodeBuddyJSONInt 读取整数值（json.Number / float64 / int 家族）。
func tencentCodeBuddyJSONInt(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil && f == float64(int64(f)) {
			return int64(f), true
		}
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// translateTencentCodeBuddyMaxCompletionTokens 把 max_completion_tokens 翻译为 max_tokens。
// 显式 max_tokens 优先；别名不是正整数时不翻译。别名无论如何都删掉。
func translateTencentCodeBuddyMaxCompletionTokens(obj map[string]any) {
	alias, has := obj["max_completion_tokens"]
	delete(obj, "max_completion_tokens")
	if !has {
		return
	}
	if _, explicit := obj["max_tokens"]; explicit {
		return
	}
	if v, ok := tencentCodeBuddyJSONInt(alias); ok && v > 0 {
		obj["max_tokens"] = v
	}
}

// clampTencentCodeBuddyGPTMinMaxTokens 把 GPT 系模型过小的 max_tokens 抬到 16。
func clampTencentCodeBuddyGPTMinMaxTokens(obj map[string]any) {
	model, _ := obj["model"].(string)
	if !strings.Contains(strings.ToLower(model), "gpt-") {
		return
	}
	v, ok := tencentCodeBuddyJSONInt(obj["max_tokens"])
	if !ok || v >= tencentCodeBuddyGPTMinMaxTokens {
		return
	}
	obj["max_tokens"] = int64(tencentCodeBuddyGPTMinMaxTokens)
}

// normalizeTencentCodeBuddyToolPatterns 把 tools 定义里正则 pattern 的 `\_` 改成 `_`。
// 所有正则引擎都把 `\_` 当作 `_`，只有上游校验器拒收；只动 tools 子树，不碰消息正文。
func normalizeTencentCodeBuddyToolPatterns(obj map[string]any) {
	tools, ok := obj["tools"].([]any)
	if !ok {
		return
	}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			unescapeTencentCodeBuddyPatternEscapes(fn["parameters"])
		}
		unescapeTencentCodeBuddyPatternEscapes(tool["parameters"])
	}
}

func unescapeTencentCodeBuddyPatternEscapes(node any) {
	switch n := node.(type) {
	case map[string]any:
		if p, ok := n["pattern"].(string); ok && strings.Contains(p, `\_`) {
			n["pattern"] = strings.ReplaceAll(p, `\_`, `_`)
		}
		if props, ok := n["patternProperties"].(map[string]any); ok {
			fixed := make(map[string]any, len(props))
			rebuilt := false
			for k, v := range props {
				if strings.Contains(k, `\_`) {
					k = strings.ReplaceAll(k, `\_`, `_`)
					rebuilt = true
				}
				fixed[k] = v
			}
			if rebuilt {
				n["patternProperties"] = fixed
			}
		}
		for _, v := range n {
			unescapeTencentCodeBuddyPatternEscapes(v)
		}
	case []any:
		for _, v := range n {
			unescapeTencentCodeBuddyPatternEscapes(v)
		}
	}
}

// normalizeTencentCodeBuddyImageURL 把字符串形态的 image_url 转成 {"url": ...}。
func normalizeTencentCodeBuddyImageURL(obj map[string]any) {
	messages, ok := obj["messages"].([]any)
	if !ok {
		return
	}
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok || part["type"] != "image_url" {
				continue
			}
			if url, ok := part["image_url"].(string); ok && url != "" {
				part["image_url"] = map[string]any{"url": url}
			}
		}
	}
}

// ===== tool_call ↔ tool 结果配对 =====

// mergeTencentCodeBuddyAdjacentToolCalls 把背靠背的 assistant 消息合成一条：
//   - 本条带 tool_calls 且没有正文，上一条也是带 tool_calls 的 assistant → 调用并入上一条；
//   - 本条是纯文本 assistant，上一条是没有正文的 tool_calls assistant → 正文折进上一条。
//
// 部分客户端回放并行调用时会拆成多条 assistant，DeepSeek 系上游会判 11148。
func mergeTencentCodeBuddyAdjacentToolCalls(messages []any) ([]any, bool) {
	if len(messages) < 2 {
		return messages, false
	}
	out := make([]any, 0, len(messages))
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" || len(out) == 0 {
			out = append(out, raw)
			continue
		}
		prev, ok := out[len(out)-1].(map[string]any)
		if !ok || prev["role"] != "assistant" {
			out = append(out, raw)
			continue
		}
		prevCalls, _ := prev["tool_calls"].([]any)
		if len(prevCalls) == 0 {
			out = append(out, raw)
			continue
		}
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 && tencentCodeBuddyEmptyContent(message["content"]) {
			prev["tool_calls"] = append(prevCalls, calls...)
			mergeTencentCodeBuddyReasoningContent(prev, message)
			changed = true
			continue
		}
		if _, hasCalls := message["tool_calls"]; !hasCalls {
			if text, ok := message["content"].(string); ok && text != "" && tencentCodeBuddyEmptyContent(prev["content"]) {
				prev["content"] = text
				mergeTencentCodeBuddyReasoningContent(prev, message)
				changed = true
				continue
			}
		}
		out = append(out, raw)
	}
	if !changed {
		return messages, false
	}
	return out, true
}

func mergeTencentCodeBuddyReasoningContent(dst, src map[string]any) {
	rc, _ := src["reasoning_content"].(string)
	if rc == "" {
		return
	}
	if prev, _ := dst["reasoning_content"].(string); prev != "" {
		dst["reasoning_content"] = prev + "\n" + rc
		return
	}
	dst["reasoning_content"] = rc
}

// tencentCodeBuddyEmptyContent 判断 content 是否为空（缺失 / null / 空串 / 空数组）。
func tencentCodeBuddyEmptyContent(v any) bool {
	switch c := v.(type) {
	case nil:
		return true
	case string:
		return c == ""
	case []any:
		return len(c) == 0
	}
	return false
}

// repackTencentCodeBuddyToolResultBlocks 把插在一批 tool 结果中间的非 tool 消息挪到这批结果之后，
// 保证 assistant.tool_calls 后面紧跟它自己的全部结果（例如 Codex 的 image_resize_notice）。
// 只调顺序、不改内容。
func repackTencentCodeBuddyToolResultBlocks(messages []any) ([]any, bool) {
	if len(messages) < 3 {
		return messages, false
	}
	out := make([]any, 0, len(messages))
	changed := false
	i := 0
	for i < len(messages) {
		message, ok := messages[i].(map[string]any)
		if !ok || message["role"] != "assistant" {
			out = append(out, messages[i])
			i++
			continue
		}
		calls, _ := message["tool_calls"].([]any)
		if len(calls) == 0 {
			out = append(out, messages[i])
			i++
			continue
		}
		want := map[string]bool{}
		for _, rawCall := range calls {
			if call, ok := rawCall.(map[string]any); ok {
				if id, _ := call["id"].(string); id != "" {
					want[id] = true
				}
			}
		}
		out = append(out, messages[i])
		i++
		var results, between []any
		for i < len(messages) {
			next, ok := messages[i].(map[string]any)
			if !ok {
				break
			}
			role, _ := next["role"].(string)
			if role == "tool" {
				id, _ := next["tool_call_id"].(string)
				if !want[id] {
					break
				}
				results = append(results, messages[i])
				if len(between) > 0 {
					changed = true
				}
				i++
				continue
			}
			if len(results) == 0 {
				break
			}
			// 下一批 tool_calls 是新的组头，不能当插入物吞掉。
			if role == "assistant" {
				if nextCalls, _ := next["tool_calls"].([]any); len(nextCalls) > 0 {
					break
				}
			}
			between = append(between, messages[i])
			i++
		}
		out = append(out, results...)
		out = append(out, between...)
	}
	if !changed {
		return messages, false
	}
	return out, true
}

// cleanupTencentCodeBuddyOrphanToolCalls 剔除无法配对的 tool_call 与 tool 结果：
// 调用侧只保留有结果的调用（全部没有结果时删掉 tool_calls 键），结果侧只保留调用仍在的结果。
// 工具执行失败时客户端常会留下没有结果的调用，不剔除的话之后每次请求都 400。
func cleanupTencentCodeBuddyOrphanToolCalls(messages []any) ([]any, bool) {
	callIDs := map[string]bool{}
	resultIDs := map[string]bool{}
	hasTraffic := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch message["role"] {
		case "tool":
			if id, _ := message["tool_call_id"].(string); id != "" {
				resultIDs[id] = true
				hasTraffic = true
			}
		case "assistant":
			calls, _ := message["tool_calls"].([]any)
			for _, rawCall := range calls {
				if call, ok := rawCall.(map[string]any); ok {
					if id, _ := call["id"].(string); id != "" {
						callIDs[id] = true
						hasTraffic = true
					}
				}
			}
		}
	}
	if !hasTraffic {
		return messages, false
	}
	keep := map[string]bool{}
	for id := range callIDs {
		if resultIDs[id] {
			keep[id] = true
		}
	}
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		calls, ok := message["tool_calls"].([]any)
		if !ok || len(calls) == 0 {
			continue
		}
		kept := make([]any, 0, len(calls))
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			if id, _ := call["id"].(string); keep[id] {
				kept = append(kept, call)
			}
		}
		if len(kept) == len(calls) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(message, "tool_calls")
			continue
		}
		message["tool_calls"] = kept
	}
	out := make([]any, 0, len(messages))
	for _, raw := range messages {
		if message, ok := raw.(map[string]any); ok && message["role"] == "tool" {
			if id, _ := message["tool_call_id"].(string); !keep[id] {
				changed = true
				continue
			}
		}
		out = append(out, raw)
	}
	if !changed {
		return messages, false
	}
	return out, true
}

// ===== DeepSeek 思维链与推理档位 =====

func isTencentCodeBuddyDeepSeekModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// injectTencentCodeBuddyThinking 对齐官方客户端的 DeepSeek "开思考"：thinking.type=enabled 加一个档位。
//   - 客户端显式 thinking.type=disabled → 尊重，并删掉 reasoning_effort；
//   - 显式 enabled → 不改，缺档位时补默认档；
//   - 没有 thinking → 注入 enabled 并补默认档（已有档位不覆盖）。
//
// 非 DeepSeek 模型不动。
func injectTencentCodeBuddyThinking(obj map[string]any, defaultEffort string) {
	model, _ := obj["model"].(string)
	if !isTencentCodeBuddyDeepSeekModel(model) {
		return
	}
	thinking, isObject := obj["thinking"].(map[string]any)
	thinkingType := ""
	if isObject {
		thinkingType, _ = thinking["type"].(string)
		thinkingType = strings.TrimSpace(thinkingType)
	}
	if thinkingType != "" {
		if strings.EqualFold(thinkingType, "disabled") {
			delete(obj, "reasoning_effort")
			delete(obj, "reasoningEffort")
			return
		}
		ensureTencentCodeBuddyDeepSeekEffort(obj, defaultEffort)
		return
	}
	if isObject {
		thinking["type"] = "enabled"
	} else {
		obj["thinking"] = map[string]any{"type": "enabled"}
	}
	ensureTencentCodeBuddyDeepSeekEffort(obj, defaultEffort)
}

func ensureTencentCodeBuddyDeepSeekEffort(obj map[string]any, defaultEffort string) {
	if _, ok := obj["reasoning_effort"]; ok {
		return
	}
	if _, ok := obj["reasoningEffort"]; ok {
		return
	}
	if defaultEffort == "" {
		defaultEffort = tencentCodeBuddyDefaultDeepSeekEffort
	}
	obj["reasoning_effort"] = defaultEffort
}

// normalizeTencentCodeBuddyReasoningEffort 把模型不支持的 reasoning_effort 降到支持的档位：
// 取不高于请求档位的最高支持档；支持档全都更高时取最低支持档。档位表未知、
// 请求档位无法识别或未携带时原样透传。
func normalizeTencentCodeBuddyReasoningEffort(obj map[string]any, supported []string) {
	if len(supported) == 0 {
		return
	}
	key := ""
	if _, ok := obj["reasoning_effort"]; ok {
		key = "reasoning_effort"
	} else if _, ok := obj["reasoningEffort"]; ok {
		key = "reasoningEffort"
	} else {
		return
	}
	requested, ok := obj[key].(string)
	if !ok {
		return
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	requestedRank, known := tencentCodeBuddyEffortRank[requested]
	if !known {
		return
	}
	best, bestRank := "", -1
	lowest, lowestRank := "", 1<<30
	for _, effort := range supported {
		rank, ok := tencentCodeBuddyEffortRank[strings.ToLower(strings.TrimSpace(effort))]
		if !ok {
			continue
		}
		if rank <= requestedRank && rank > bestRank {
			best, bestRank = effort, rank
		}
		if rank < lowestRank {
			lowest, lowestRank = effort, rank
		}
	}
	target := best
	if target == "" {
		target = lowest
	}
	if target == "" || strings.EqualFold(target, requested) {
		return
	}
	obj[key] = target
	model, _ := obj["model"].(string)
	logger.L().Debug("codebuddy reasoning_effort downgraded",
		zap.String("model", model), zap.String("from", requested), zap.String("to", target))
}

// backfillTencentCodeBuddyReasoningContent 保证 DeepSeek 会话里每条 assistant 都带非空的
// reasoning_content / reasoning（官方客户端 requiresReasoningContentOnAssistantMessages）。
// 已有内容不覆盖；两者都没有时补单个空格占位（上游校验 len>0，空串不过）。
// 思考关闭且历史里没有任何推理痕迹时不动。
func backfillTencentCodeBuddyReasoningContent(obj map[string]any) {
	model, _ := obj["model"].(string)
	if !isTencentCodeBuddyDeepSeekModel(model) {
		return
	}
	messages, ok := obj["messages"].([]any)
	if !ok || len(messages) == 0 {
		return
	}
	enabled := false
	if thinking, ok := obj["thinking"].(map[string]any); ok {
		if t, _ := thinking["type"].(string); strings.EqualFold(strings.TrimSpace(t), "enabled") {
			enabled = true
		}
	}
	hasTrace := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if r, _ := message["reasoning"].(string); r != "" {
			hasTrace = true
			break
		}
		if _, ok := message["reasoning_content"]; ok {
			hasTrace = true
			break
		}
	}
	if !enabled && !hasTrace {
		return
	}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		rc, hasRC := message["reasoning_content"].(string)
		if !hasRC {
			if r, ok := message["reasoning"].(string); ok {
				rc = r
			}
			message["reasoning_content"] = rc
		}
		if r, _ := message["reasoning"].(string); r != "" {
			continue
		}
		if rc != "" {
			message["reasoning"] = rc
		} else {
			message["reasoning"] = " "
		}
	}
}

// ===== prompt_cache_key =====

// injectTencentCodeBuddyPromptCacheKey 注入按账号隔离的 prompt_cache_key。
// 客户端已带键时不覆盖；会话源优先取 body 里的 conversation_id，其次是调用方传入的会话标识。
// 键里带账号 uid 段，跨账号绝不相同，避免命中别的账号的前缀缓存。
func injectTencentCodeBuddyPromptCacheKey(obj map[string]any, uid, conversationID string) {
	if existing, _ := obj["prompt_cache_key"].(string); strings.TrimSpace(existing) != "" {
		return
	}
	conversation := strings.TrimSpace(conversationID)
	for _, key := range []string{"conversation_id", "conversationId"} {
		if v, _ := obj[key].(string); strings.TrimSpace(v) != "" {
			conversation = strings.TrimSpace(v)
			break
		}
	}
	obj["prompt_cache_key"] = buildTencentCodeBuddyPromptCacheKey(uid, conversation)
}

func buildTencentCodeBuddyPromptCacheKey(uid, conversation string) string {
	uid8 := uid
	if len(uid8) > 8 {
		uid8 = uid8[:8]
	}
	if uid8 == "" {
		uid8 = "-"
	}
	sum := sha256.Sum256([]byte(uid + "|" + conversation))
	return "cb-" + uid8 + "-" + hex.EncodeToString(sum[:16])
}

// tencentCodeBuddyConversationID 从客户端请求头里取会话标识（Codex / 各类 agent 客户端的常见写法）。
func tencentCodeBuddyConversationID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	for _, key := range []string{"X-Conversation-ID", "Conversation_id", "Conversation-ID", "Session_id", "X-Session-ID", "Session-ID"} {
		if v := strings.TrimSpace(c.Request.Header.Get(key)); v != "" {
			return v
		}
	}
	return ""
}
