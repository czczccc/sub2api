package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 本文件处理 CodeBuddy / WorkBuddy 响应侧的兼容：缓存命中用量的别名统一、
// 以及给常见上游错误附加网关视角的说明（移植自参考实现 usage.go / hint.go）。

// tencentCodeBuddyUsageCachePaths 是 usage 里各种缓存命中写法，按可信度排序。
var tencentCodeBuddyUsageCachePaths = []string{
	"prompt_tokens_details.cached_tokens",
	"prompt_cache_hit_tokens",
	"cache_read_input_tokens",
	"cached_tokens",
	"input_tokens_details.cached_tokens",
}

// normalizeTencentCodeBuddyUsageCacheAliases 统一 JSON 对象里 usage 的缓存命中别名。
//
// WorkBuddy 有时把真实命中放在 prompt_tokens_details.cached_tokens（或 prompt_cache_hit_tokens），
// 同时又带着值为 0 的 cache_read_input_tokens / cached_tokens 兼容字段。下游严格的解析器
// 可能优先读到 0 而丢掉命中，这里把所有别名改成同一个真实值。没有命中时不改。
func normalizeTencentCodeBuddyUsageCacheAliases(payload []byte) []byte {
	usage := gjson.GetBytes(payload, "usage")
	if !usage.IsObject() {
		return payload
	}
	var best int64
	for _, path := range tencentCodeBuddyUsageCachePaths {
		if v := usage.Get(path).Int(); v > 0 {
			best = v
			break
		}
	}
	if best <= 0 {
		return payload
	}
	paths := []string{"cache_read_input_tokens", "cached_tokens", "prompt_cache_hit_tokens", "prompt_tokens_details.cached_tokens"}
	// Responses 形态的嵌套字段只在上游本来就带时才改，不为 Chat 客户端凭空造。
	if usage.Get("input_tokens_details").Exists() {
		paths = append(paths, "input_tokens_details.cached_tokens")
	}
	out := payload
	for _, path := range paths {
		if usage.Get(path).Int() == best {
			continue
		}
		if updated, err := sjson.SetBytes(out, "usage."+path, best); err == nil {
			out = updated
		}
	}
	return out
}

// applyTencentCodeBuddyUsageSSELine 对 CodeBuddy 流式响应中带 usage 的 data 行做别名统一。
func applyTencentCodeBuddyUsageSSELine(account *Account, line string) string {
	if account == nil || !account.IsTencentCodeBuddy() || !strings.Contains(line, `"usage"`) {
		return line
	}
	payload, ok := extractOpenAISSEDataLine(line)
	if !ok || !gjson.Valid(payload) {
		return line
	}
	normalized := normalizeTencentCodeBuddyUsageCacheAliases([]byte(payload))
	if len(normalized) == len(payload) && string(normalized) == payload {
		return line
	}
	return "data: " + string(normalized)
}

// applyTencentCodeBuddyUsageBody 对 CodeBuddy 非流式响应体做别名统一。
func applyTencentCodeBuddyUsageBody(account *Account, body []byte) []byte {
	if account == nil || !account.IsTencentCodeBuddy() || !gjson.ValidBytes(body) {
		return body
	}
	return normalizeTencentCodeBuddyUsageCacheAliases(body)
}

// tencentCodeBuddyGatewayHint 按上游错误返回给客户端的补充说明（英文，面向客户端工具链）；
// 未覆盖的形态返回空串。上游原文始终保留，提示只追加在后面。
func tencentCodeBuddyGatewayHint(status int, body []byte) string {
	lower := strings.ToLower(string(body))
	code := tencentCodeBuddyBusinessCode(body)
	switch {
	case code == "11133" || strings.Contains(lower, "model_param_invalid"):
		return "request parameters were rejected by the model provider; check message format and model capabilities (images, max_tokens, reasoning_effort)"
	case code == "11135" || strings.Contains(lower, "invalid_image_data") || strings.Contains(lower, "replace the image"):
		return "image data rejected by upstream; use a real/valid image, may need a new conversation"
	case code == "11115" || strings.Contains(lower, "prompt is too long") || strings.Contains(lower, "context length"):
		return "request context exceeds the model's limit; reduce history/message size"
	case code == "11148" || strings.Contains(lower, "tool_call_sequence_broken"):
		return "tool calls and tool results in history do not match; start a new conversation"
	case code == "11129":
		return "tool definitions were rejected by upstream schema validation; check tools[].function.parameters"
	case code == "11128" || (status == http.StatusForbidden && strings.Contains(lower, "unapproved channel")):
		return "request content was rejected by content policy; adjust the prompt and retry"
	case code == "11102":
		return "this model is not available on the selected account; switch model or retry"
	case code == "6004":
		return "model usage limit reached; retry after reset or switch model"
	case code == "14017" || strings.Contains(lower, "trial not activated"):
		return "WorkBuddy global account is not activated yet; the gateway activates it automatically, retry in a minute"
	case code == "14018" || status == http.StatusPaymentRequired:
		return "account credits exhausted; waiting for daily check-in to restore"
	}
	return ""
}

// appendTencentCodeBuddyGatewayHint 在返回给客户端的错误信息后追加网关提示。
func appendTencentCodeBuddyGatewayHint(account *Account, status int, body []byte, message string) string {
	if account == nil || !account.IsTencentCodeBuddy() {
		return message
	}
	hint := tencentCodeBuddyGatewayHint(status, body)
	if hint == "" {
		return message
	}
	return message + " (gateway hint: " + hint + ")"
}

// isTencentCodeBuddyTruncatedArguments 判断工具参数是否是被截断的半截 JSON：
// 空串是合法的无参调用；能解析的任何 JSON 都不算截断（类型不对交给客户端校验）。
func isTencentCodeBuddyTruncatedArguments(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	return !gjson.Valid(trimmed)
}
