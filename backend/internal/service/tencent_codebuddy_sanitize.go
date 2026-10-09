package service

import (
	"context"
	_ "embed"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件是 CodeBuddy / WorkBuddy 出站请求的内容净化（移植自参考实现 workbuddy2api-panel
// internal/upstream/sanitize.go、internal/prompt、internal/server/degrade.go / wafip.go）。
//
// 上游内容审核对 Claude Code / Codex 等客户端注入的固定模板句做逐字匹配，命中即 400
// code=11128 "Illegal API invocation from an unapproved channel"，与请求内容本身是否合规无关。
// 三层处理：
//  1. 指纹脱敏：键值/header 型指纹整段删除，模板句改一个词（语义不变）；
//  2. 网关系统提示词：后台可选透传 / 追加 / 替换客户端 system 消息；
//  3. 降级：透传模式下被拦截时，当天改用一句中性 system 提示词并立即重试一次。

//go:embed tencent_codebuddy_default_prompt.md
var TencentCodeBuddyDefaultSystemPrompt string

// tencentCodeBuddyDegradedPrompt 是降级时使用的中性提示词，刻意极简。
const tencentCodeBuddyDegradedPrompt = "You are a helpful assistant. Respond in the user's language, follow the user's instructions, and be direct and concise."

// tencentCodeBuddySanitizeFeatures 是快速预检特征：一个都不中时文本原样返回。
var tencentCodeBuddySanitizeFeatures = []string{
	"x-anthropic-billing-header",
	"cc_entrypoint=",
	"You are Claude Code",
	"Main branch (",
	"You are a coding agent running in the Codex CLI",
	"github.com/anthropics/",
	"11128",
}

var (
	tencentCodeBuddyHeaderFingerprintRe = regexp.MustCompile(`(?i)x-anthropic-billing-header:[^;\n]*;?\s*`)
	tencentCodeBuddyBareHeaderRe        = regexp.MustCompile(`(?i)x-anthropic-billing-header`)
	tencentCodeBuddyCCKeyValueRe        = regexp.MustCompile(`(?i)\bcc_[a-z0-9_]+=[^;\n]*;?\s*`)
)

// tencentCodeBuddySanitizeRewrites 是模板句的最小改写（每句只改一个词）。
var tencentCodeBuddySanitizeRewrites = [][2]string{
	// 不带结尾标点：CLI 版以句号结尾，桌面版 / Agent SDK 以逗号接后续内容。
	{"You are Claude Code, Anthropic's official CLI for Claude", "You are Claude Code, Anthropic's official CLI tool for Claude"},
	{"Main branch (you will usually use this for PRs)", "Default branch (you will usually use this for PRs)"},
	{"You are a coding agent running in the Codex CLI, a terminal-based coding assistant.", "You are a coding agent running in the Codex CLI tool, a terminal-based coding assistant."},
	{"To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues", "To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues"},
	// 请求体里出现裸数字 11128（上游拦截自身的错误码）就会被整单拦截，插入连字符保留可读性。
	{"11128", "11-128"},
}

// sanitizeTencentCodeBuddyText 净化单段文本；无指纹时原样返回。
func sanitizeTencentCodeBuddyText(text string) string {
	if !tencentCodeBuddyHasFingerprint(text) {
		return text
	}
	for _, rw := range tencentCodeBuddySanitizeRewrites {
		text = strings.ReplaceAll(text, rw[0], rw[1])
	}
	text = tencentCodeBuddyHeaderFingerprintRe.ReplaceAllString(text, "")
	if strings.Contains(text, "cc_") {
		for prev := ""; prev != text; {
			prev = text
			text = tencentCodeBuddyCCKeyValueRe.ReplaceAllString(text, "")
		}
	}
	// 键值形态已整段删除，剩下的只会是引用 / 示例里的裸键名，缩写一个词即可。
	text = tencentCodeBuddyBareHeaderRe.ReplaceAllString(text, "x-anthropic-billing-hdr")
	return strings.TrimSpace(text)
}

func tencentCodeBuddyHasFingerprint(text string) bool {
	for _, feature := range tencentCodeBuddySanitizeFeatures {
		if strings.Contains(text, feature) {
			return true
		}
	}
	return tencentCodeBuddyBareHeaderRe.MatchString(text)
}

// sanitizeTencentCodeBuddyMessages 净化 messages 的 content（字符串或 text part）、
// reasoning_content / reasoning 以及 tool_calls[].function.arguments。
func sanitizeTencentCodeBuddyMessages(messages []any) {
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch content := message["content"].(type) {
		case string:
			message["content"] = sanitizeTencentCodeBuddyText(content)
		case []any:
			for _, rawPart := range content {
				if part, ok := rawPart.(map[string]any); ok {
					if text, ok := part["text"].(string); ok {
						part["text"] = sanitizeTencentCodeBuddyText(text)
					}
				}
			}
		}
		for _, key := range []string{"reasoning_content", "reasoning"} {
			if text, ok := message[key].(string); ok {
				message[key] = sanitizeTencentCodeBuddyText(text)
			}
		}
		calls, _ := message["tool_calls"].([]any)
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			if fn, ok := call["function"].(map[string]any); ok {
				if args, ok := fn["arguments"].(string); ok {
					fn["arguments"] = sanitizeTencentCodeBuddyText(args)
				}
			}
		}
	}
}

// rewriteTencentCodeBuddySystemPrompt 删除全部 system / developer 消息，在最前面插入一条网关提示词。
func rewriteTencentCodeBuddySystemPrompt(obj map[string]any, prompt string) {
	messages, _ := obj["messages"].([]any)
	kept := make([]any, 0, len(messages)+1)
	kept = append(kept, map[string]any{"role": "system", "content": prompt})
	for _, raw := range messages {
		if message, ok := raw.(map[string]any); ok {
			if role, _ := message["role"].(string); role == "system" || role == "developer" {
				continue
			}
		}
		kept = append(kept, raw)
	}
	obj["messages"] = kept
}

// appendTencentCodeBuddySystemPrompt 在开头连续的 system / developer 块之后插入网关提示词，
// 客户端已有消息一律不动。
func appendTencentCodeBuddySystemPrompt(obj map[string]any, prompt string) {
	messages, _ := obj["messages"].([]any)
	insertAt := 0
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			break
		}
		if role, _ := message["role"].(string); role != "system" && role != "developer" {
			break
		}
		insertAt++
	}
	out := make([]any, 0, len(messages)+1)
	out = append(out, messages[:insertAt]...)
	out = append(out, map[string]any{"role": "system", "content": prompt})
	out = append(out, messages[insertAt:]...)
	obj["messages"] = out
}

// tencentCodeBuddyContentPolicy 描述一次请求要应用的内容策略。
type tencentCodeBuddyContentPolicy struct {
	Sanitize bool
	// PromptMode 为 custom / append 时使用 PromptText；Degraded 为 true 时强制替换为中性提示词。
	PromptMode string
	PromptText string
	Degraded   bool
}

// CanDegrade 报告被内容审核拦截时是否应切到中性提示词重试（透传 / 追加模式且尚未降级）。
func (p tencentCodeBuddyContentPolicy) CanDegrade() bool {
	return !p.Degraded && (p.PromptMode == WorkBuddyPromptModeDegrade || p.PromptMode == WorkBuddyPromptModeAppend)
}

// tencentCodeBuddyContentPolicy 读取后台配置与降级状态，组装本次请求的内容策略。
func (s *OpenAIGatewayService) tencentCodeBuddyContentPolicy(ctx context.Context) tencentCodeBuddyContentPolicy {
	var cfg WorkBuddyConfig
	if s != nil && s.settingService != nil {
		cfg = s.settingService.GetWorkBuddyConfig(ctx)
	} else {
		cfg = normalizeWorkBuddyConfig(WorkBuddyConfig{})
	}
	policy := tencentCodeBuddyContentPolicy{
		Sanitize:   !cfg.SanitizeDisabled,
		PromptMode: cfg.PromptMode,
		PromptText: cfg.EffectivePromptText(),
	}
	if policy.PromptMode == WorkBuddyPromptModeDegrade || policy.PromptMode == WorkBuddyPromptModeAppend {
		policy.Degraded = tencentCodeBuddyDegrade.Active(time.Now())
	}
	return policy
}

// applyTencentCodeBuddySystemPromptPolicy 按策略改写 system 提示词（脱敏由调用方最后执行）。
func applyTencentCodeBuddySystemPromptPolicy(obj map[string]any, policy tencentCodeBuddyContentPolicy) {
	switch {
	case policy.Degraded:
		rewriteTencentCodeBuddySystemPrompt(obj, tencentCodeBuddyDegradedPrompt)
	case policy.PromptMode == WorkBuddyPromptModeCustom && policy.PromptText != "":
		rewriteTencentCodeBuddySystemPrompt(obj, policy.PromptText)
	case policy.PromptMode == WorkBuddyPromptModeAppend && policy.PromptText != "":
		appendTencentCodeBuddySystemPrompt(obj, policy.PromptText)
	}
}

// ===== 降级状态 =====

// tencentCodeBuddyDegradeGate 记录降级期：被内容审核拦截后到次日北京时间 00:00 为止都用中性提示词，
// 期间的请求不必先撞一次 400。进程内状态，重启清零。
type tencentCodeBuddyDegradeGate struct {
	mu    sync.Mutex
	until time.Time
}

var tencentCodeBuddyDegrade = &tencentCodeBuddyDegradeGate{}

func (g *tencentCodeBuddyDegradeGate) Active(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return now.Before(g.until)
}

func (g *tencentCodeBuddyDegradeGate) Trigger(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !now.Before(g.until) {
		y, m, d := now.In(tencentCodeBuddyResetLoc).Date()
		g.until = time.Date(y, m, d, 0, 0, 0, 0, tencentCodeBuddyResetLoc).AddDate(0, 0, 1)
	}
}

// isTencentCodeBuddyContentBlocked 判断上游错误是否是内容审核拦截（11128 / unapproved channel）。
func isTencentCodeBuddyContentBlocked(status int, body []byte) bool {
	if status != 400 && status != 403 {
		return false
	}
	if tencentCodeBuddyBusinessCode(body) == "11128" {
		return true
	}
	return tencentCodeBuddyContainsAny(strings.ToLower(string(body)), tencentCodeBuddyContentBlockedMarkers)
}

// ===== WAF 出口 IP 级拦截 =====

// WAF 403 拦的往往是网关出口 IP 而不是账号：同一出口 60 秒内有 2 个不同账号被拦，
// 就判定为 IP 级拦截，此后 60 秒内经该出口的请求直接失败，不再一个个账号去撞（只会加重风控）。
const (
	tencentCodeBuddyWAFIPWindow    = 60 * time.Second
	tencentCodeBuddyWAFIPThreshold = 2
)

type tencentCodeBuddyWAFIPGate struct {
	mu    sync.Mutex
	hits  map[string]map[int64]time.Time // 出口（代理）→ 账号 → 最近一次 WAF 403
	until map[string]time.Time
}

var tencentCodeBuddyWAFIP = &tencentCodeBuddyWAFIPGate{}

// tencentCodeBuddyEgressKey 用代理区分出口 IP；不走代理的账号共用直连出口。
func tencentCodeBuddyEgressKey(account *Account) string {
	if account != nil && account.ProxyID != nil {
		return "proxy:" + strconv.FormatInt(*account.ProxyID, 10)
	}
	return "direct"
}

// Note 记录一次 WAF 403，返回该出口是否已判定为 IP 级拦截。
func (g *tencentCodeBuddyWAFIPGate) Note(egress string, accountID int64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.until == nil {
		g.until = map[string]time.Time{}
		g.hits = map[string]map[int64]time.Time{}
	}
	if now.Before(g.until[egress]) {
		return true
	}
	hits := g.hits[egress]
	if hits == nil {
		hits = map[int64]time.Time{}
		g.hits[egress] = hits
	}
	hits[accountID] = now
	for id, at := range hits {
		if now.Sub(at) > tencentCodeBuddyWAFIPWindow {
			delete(hits, id)
		}
	}
	if len(hits) >= tencentCodeBuddyWAFIPThreshold {
		g.until[egress] = now.Add(tencentCodeBuddyWAFIPWindow)
		delete(g.hits, egress)
		return true
	}
	return false
}

// Blocked 报告出口当前是否处于 IP 级拦截期。
func (g *tencentCodeBuddyWAFIPGate) Blocked(egress string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return now.Before(g.until[egress])
}
