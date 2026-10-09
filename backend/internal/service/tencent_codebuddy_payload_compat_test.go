//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func codeBuddyPayloadAccount() *Account {
	return &Account{
		ID:       7,
		Platform: PlatformTencentCodeBuddy,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"access_token": "at",
			"uid":          "uid-1234567890",
		},
	}
}

func TestPrepareTencentCodeBuddyChatPayload_TokenFields(t *testing.T) {
	account := codeBuddyPayloadAccount()

	out := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"glm-5.3","max_completion_tokens":128000,"seed":9007199254740993,"messages":[]}`), account, "")
	require.False(t, gjson.GetBytes(out, "max_completion_tokens").Exists())
	require.Equal(t, int64(128000), gjson.GetBytes(out, "max_tokens").Int())
	require.Equal(t, "9007199254740993", gjson.GetBytes(out, "seed").Raw, "大整数不能丢精度")

	// 显式 max_tokens 优先，别名只删。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"glm-5.3","max_tokens":100,"max_completion_tokens":200}`), account, "")
	require.Equal(t, int64(100), gjson.GetBytes(out, "max_tokens").Int())

	// GPT 系 max_tokens < 16 抬到 16；其他模型不动。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"gpt-5.6-sol","max_tokens":1}`), account, "")
	require.Equal(t, int64(16), gjson.GetBytes(out, "max_tokens").Int())
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3","max_tokens":1}`), account, "")
	require.Equal(t, int64(1), gjson.GetBytes(out, "max_tokens").Int())

	// 流式请求补 include_usage；客户端显式给了就不覆盖。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3","stream":true}`), account, "")
	require.True(t, gjson.GetBytes(out, "stream_options.include_usage").Bool())
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3","stream":true,"stream_options":{"include_usage":false}}`), account, "")
	require.False(t, gjson.GetBytes(out, "stream_options.include_usage").Bool())
}

func TestPrepareTencentCodeBuddyChatPayload_ImageAndToolPattern(t *testing.T) {
	body := `{"model":"hy3","messages":[{"role":"user","content":[{"type":"image_url","image_url":"data:image/png;base64,AAAA"},{"type":"image_url","image_url":{"url":"https://x/y.png","detail":"low"}}]}],` +
		`"tools":[{"type":"function","function":{"name":"run","parameters":{"type":"object","properties":{"id":{"type":"string","pattern":"^agent\\_run\\_"}}}}}]}`
	out := prepareTencentCodeBuddyChatPayload([]byte(body), codeBuddyPayloadAccount(), "")
	require.Equal(t, "data:image/png;base64,AAAA", gjson.GetBytes(out, "messages.0.content.0.image_url.url").String())
	require.Equal(t, "low", gjson.GetBytes(out, "messages.0.content.1.image_url.detail").String())
	require.Equal(t, "^agent_run_", gjson.GetBytes(out, "tools.0.function.parameters.properties.id.pattern").String())
}

func TestPrepareTencentCodeBuddyChatPayload_ToolPairing(t *testing.T) {
	call := func(id string) string {
		return `{"id":"` + id + `","type":"function","function":{"name":"f","arguments":"{}"}}`
	}
	// 背靠背两条 tool_calls + 结果中间插了一条 developer + 一个没有结果的孤儿调用。
	body := `{"model":"deepseek-v4.1-flash","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":null,"tool_calls":[` + call("c0") + `]},` +
		`{"role":"assistant","content":"","tool_calls":[` + call("c1") + `,` + call("orphan") + `]},` +
		`{"role":"tool","tool_call_id":"c0","content":"r0"},` +
		`{"role":"system","content":"<image_resize_notice>"},` +
		`{"role":"tool","tool_call_id":"c1","content":"r1"},` +
		`{"role":"tool","tool_call_id":"ghost","content":"no call"}` +
		`]}`
	out := prepareTencentCodeBuddyChatPayload([]byte(body), codeBuddyPayloadAccount(), "")
	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 5)
	require.Equal(t, "assistant", messages[1].Get("role").String())
	ids := []string{}
	for _, c := range messages[1].Get("tool_calls").Array() {
		ids = append(ids, c.Get("id").String())
	}
	require.Equal(t, []string{"c0", "c1"}, ids, "合并且剔除孤儿调用")
	require.Equal(t, "c0", messages[2].Get("tool_call_id").String())
	require.Equal(t, "c1", messages[3].Get("tool_call_id").String())
	require.Equal(t, "system", messages[4].Get("role").String(), "插在中间的消息挪到结果之后")
}

func TestPrepareTencentCodeBuddyChatPayload_DeepSeekThinking(t *testing.T) {
	account := codeBuddyPayloadAccount()

	// 没有 thinking：注入 enabled + 默认档 high，并给历史 assistant 回填 reasoning。
	out := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`), account, "")
	require.Equal(t, "enabled", gjson.GetBytes(out, "thinking.type").String())
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning_effort").String())
	require.Equal(t, "", gjson.GetBytes(out, "messages.1.reasoning_content").String())
	require.True(t, gjson.GetBytes(out, "messages.1.reasoning_content").Exists())
	require.Equal(t, " ", gjson.GetBytes(out, "messages.1.reasoning").String())

	// 已有 reasoning_content 不覆盖，并镜像到 reasoning。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"assistant","content":"b","reasoning_content":"think"}]}`), account, "")
	require.Equal(t, "think", gjson.GetBytes(out, "messages.0.reasoning_content").String())
	require.Equal(t, "think", gjson.GetBytes(out, "messages.0.reasoning").String())

	// 显式 disabled：尊重，删掉档位，不回填。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"deepseek-v4.1-flash","thinking":{"type":"disabled"},"reasoning_effort":"high","messages":[{"role":"assistant","content":"b"}]}`), account, "")
	require.Equal(t, "disabled", gjson.GetBytes(out, "thinking.type").String())
	require.False(t, gjson.GetBytes(out, "reasoning_effort").Exists())
	require.False(t, gjson.GetBytes(out, "messages.0.reasoning_content").Exists())

	// 非 DeepSeek 模型不注入。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3","messages":[]}`), account, "")
	require.False(t, gjson.GetBytes(out, "thinking").Exists())
	require.False(t, gjson.GetBytes(out, "reasoning_effort").Exists())
}

func TestPrepareTencentCodeBuddyChatPayload_EffortDowngrade(t *testing.T) {
	account := codeBuddyPayloadAccount()
	cases := []struct{ model, requested, want string }{
		{"deepseek-v4.1-flash", "medium", "low"}, // low/high/max：≤medium 的最高档是 low
		{"deepseek-v4.1-flash", "xhigh", "high"},
		{"glm-5.2", "low", "high"}, // 支持档全高于请求档：取最低档
		{"glm-5.2", "max", "xhigh"},
		{"glm-5.3", "high", "high"},
		{"unknown-model", "max", "max"}, // 档位未知：透传
	}
	for _, tc := range cases {
		out := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"`+tc.model+`","reasoning_effort":"`+tc.requested+`"}`), account, "")
		require.Equal(t, tc.want, gjson.GetBytes(out, "reasoning_effort").String(), tc.model+" "+tc.requested)
	}

	// 上游快照声明的档位优先于内置表。
	account.Extra = map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
		Models: map[string]TencentCodeBuddyModelCapability{"glm-5.2": {SupportedEfforts: []string{"low"}}},
	}}
	out := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"glm-5.2","reasoning_effort":"high"}`), account, "")
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning_effort").String())
}

func TestPrepareTencentCodeBuddyChatPayload_PromptCacheKey(t *testing.T) {
	account := codeBuddyPayloadAccount()
	out := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3"}`), account, "conv-1")
	key := gjson.GetBytes(out, "prompt_cache_key").String()
	require.Regexp(t, `^cb-uid-1234-[0-9a-f]{32}$`, key)

	// 同账号同会话稳定；换会话或换账号都不同。
	again := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3"}`), account, "conv-1")
	require.Equal(t, key, gjson.GetBytes(again, "prompt_cache_key").String())
	other := prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3"}`), account, "conv-2")
	require.NotEqual(t, key, gjson.GetBytes(other, "prompt_cache_key").String())
	otherAccount := codeBuddyPayloadAccount()
	otherAccount.Credentials["uid"] = "uid-other"
	require.NotEqual(t, key, gjson.GetBytes(prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3"}`), otherAccount, "conv-1"), "prompt_cache_key").String())

	// 客户端自带的键不覆盖。
	out = prepareTencentCodeBuddyChatPayload([]byte(`{"model":"hy3","prompt_cache_key":"mine"}`), account, "conv-1")
	require.Equal(t, "mine", gjson.GetBytes(out, "prompt_cache_key").String())
}

func TestPrepareTencentCodeBuddyChatPayload_InvalidBodyUnchanged(t *testing.T) {
	require.Equal(t, []byte(`not json`), prepareTencentCodeBuddyChatPayload([]byte(`not json`), codeBuddyPayloadAccount(), ""))
}

func TestNormalizeTencentCodeBuddyUsageCacheAliases(t *testing.T) {
	payload := []byte(`{"usage":{"prompt_tokens":8000,"prompt_tokens_details":{"cached_tokens":7808},"cache_read_input_tokens":0,"cached_tokens":0}}`)
	out := normalizeTencentCodeBuddyUsageCacheAliases(payload)
	for _, path := range []string{"usage.cache_read_input_tokens", "usage.cached_tokens", "usage.prompt_cache_hit_tokens", "usage.prompt_tokens_details.cached_tokens"} {
		require.Equal(t, int64(7808), gjson.GetBytes(out, path).Int(), path)
	}
	require.False(t, gjson.GetBytes(out, "usage.input_tokens_details").Exists())

	noHit := []byte(`{"usage":{"prompt_tokens":10,"cached_tokens":0}}`)
	require.Equal(t, noHit, normalizeTencentCodeBuddyUsageCacheAliases(noHit))

	line := applyTencentCodeBuddyUsageSSELine(codeBuddyPayloadAccount(), `data: {"choices":[],"usage":{"prompt_cache_hit_tokens":50,"prompt_tokens_details":{"cached_tokens":0}}}`)
	require.Contains(t, line, `"cached_tokens":50`)
	other := &Account{Platform: PlatformOpenAI}
	raw := `data: {"usage":{"prompt_cache_hit_tokens":50}}`
	require.Equal(t, raw, applyTencentCodeBuddyUsageSSELine(other, raw))
}

// 计费侧：嵌套 cached_tokens=0 但 prompt_cache_hit_tokens 有值时，按命中计。
func TestOpenAICacheReadTokensFromUsage_PromptCacheHitTokens(t *testing.T) {
	usage := gjson.Parse(`{"prompt_tokens":8000,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":7808}`)
	require.Equal(t, 7808, openAICacheReadTokensFromUsage(usage))
	usage = gjson.Parse(`{"prompt_tokens":8000,"prompt_cache_hit_tokens":512}`)
	require.Equal(t, 512, openAICacheReadTokensFromUsage(usage))
	usage = gjson.Parse(`{"prompt_tokens":8000,"prompt_tokens_details":{"cached_tokens":100}}`)
	require.Equal(t, 100, openAICacheReadTokensFromUsage(usage))
}

func TestTencentCodeBuddyGatewayHint(t *testing.T) {
	account := codeBuddyPayloadAccount()
	msg := appendTencentCodeBuddyGatewayHint(account, http.StatusBadRequest, []byte(`{"code":11133,"msg":"model_param_invalid"}`), "bad")
	require.Contains(t, msg, "bad (gateway hint: request parameters were rejected")
	msg = appendTencentCodeBuddyGatewayHint(account, http.StatusBadRequest, []byte(`{"code":11115,"msg":"too long"}`), "x")
	require.Contains(t, msg, "context exceeds")
	require.Equal(t, "x", appendTencentCodeBuddyGatewayHint(account, http.StatusBadRequest, []byte(`{"code":1}`), "x"))
	require.Equal(t, "x", appendTencentCodeBuddyGatewayHint(&Account{Platform: PlatformOpenAI}, http.StatusBadRequest, []byte(`{"code":11133}`), "x"))
}

func TestIsTencentCodeBuddyTruncatedArguments(t *testing.T) {
	require.False(t, isTencentCodeBuddyTruncatedArguments(""))
	require.False(t, isTencentCodeBuddyTruncatedArguments(`{"a":1}`))
	require.False(t, isTencentCodeBuddyTruncatedArguments(`[]`))
	require.True(t, isTencentCodeBuddyTruncatedArguments(`{"a":`))
}

func TestParseTencentCodeBuddyModelCapabilities_Efforts(t *testing.T) {
	caps := parseTencentCodeBuddyModelCapabilities([]byte(`{"data":{"models":[` +
		`{"id":"a","reasoning":{"supportedEfforts":["low","HIGH"],"defaultEffort":"high"}},` +
		`{"id":"b","reasoning":{"supportedEfforts":["medium"],"effort":"medium"}},` +
		`{"id":"c","reasoning":{"supportedEfforts":["low"],"defaultEffort":"max"}}]}}`))
	require.Equal(t, []string{"low", "high"}, caps["a"].SupportedEfforts)
	require.Equal(t, "high", caps["a"].DefaultEffort)
	require.Equal(t, "medium", caps["b"].DefaultEffort, "老字段 effort 兜底")
	require.Empty(t, caps["c"].DefaultEffort, "不在档位表里的默认档不采信")
}

func TestResolveTencentCodeBuddyGroupModelCapability_EffortsIntersect(t *testing.T) {
	a := *codeBuddyPayloadAccount()
	b := *codeBuddyPayloadAccount()
	a.Extra = map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
		Models: map[string]TencentCodeBuddyModelCapability{"m": {SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high"}},
	}}
	b.Extra = map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
		Models: map[string]TencentCodeBuddyModelCapability{"m": {SupportedEfforts: []string{"high", "max"}, DefaultEffort: "max"}},
	}}
	capability, _ := ResolveTencentCodeBuddyGroupModelCapability([]Account{a, b}, "m")
	require.Equal(t, []string{"high"}, capability.SupportedEfforts)
	require.Equal(t, "high", capability.DefaultEffort)
}
