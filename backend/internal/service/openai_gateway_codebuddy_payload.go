package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 本文件处理腾讯 CodeBuddy 私有上游对请求体的**额外字段约束**。
//
// 上游 /v2/* 是官方 CLI/插件使用的非公开接口，它对请求体做比 OpenAI 规范更严格的
// 校验，命中即返回 HTTP 400（业务码不同）。下表来自参考实现
// Sliverkiss/workbuddy2api internal/upstream/payload.go 的实测结论：
//
//	code=11128 "Illegal API invocation from an unapproved channel"
//	  ← messages[].role == "developer"（OpenAI 新规范里 system 的别名，
//	    Codex / Cursor / Pi 等新客户端用它承载 system 级指令；上游 role 白名单不含它）
//
// 这些是**协议兼容**修正（补齐上游白名单），不是内容脱敏：不改语义、不删消息，
// 因此对所有 CodeBuddy 账号无条件生效。
//
// 用 gjson/sjson 做定点改写而不是整体 json.Unmarshal/Marshal，避免重排键、
// 改变数字表示等副作用影响穿透式转发。

// normalizeTencentCodeBuddyUpstreamPayload 归一化 CodeBuddy 上游请求体。
// 无法解析的 body 原样返回（交给上游报错，不在此吞掉）。
func normalizeTencentCodeBuddyUpstreamPayload(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	body = normalizeTencentCodeBuddyMessageRoles(body)
	body = normalizeTencentCodeBuddyToolChoice(body)
	return body
}

// normalizeTencentCodeBuddyMessageRoles 把 messages[].role 的 developer 归一为 system。
//
// 只认 developer 这一个值：其余 role（system/user/assistant/tool 及未知值）一律原样保留，
// 不合并、不重排、不删除任何消息。
func normalizeTencentCodeBuddyMessageRoles(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	for index, message := range messages.Array() {
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "developer") {
			continue
		}
		updated, err := sjson.SetBytes(body, fmt.Sprintf("messages.%d.role", index), "system")
		if err != nil {
			// 单条改写失败不阻断：其余消息仍继续归一化。
			continue
		}
		body = updated
	}
	return body
}

// normalizeTencentCodeBuddyToolChoice 按上游接受的形态改写 tool_choice。
//
// 上游的 tool_choice 是字符串类型，OpenAI 的对象形式会被判为非法参数；
// "none" 语义上等于"不带工具"，因此连同 tools/functions 一起删除。
func normalizeTencentCodeBuddyToolChoice(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() {
		return body
	}

	dropTools := func(doc []byte) []byte {
		doc, _ = sjson.DeleteBytes(doc, "tool_choice")
		doc, _ = sjson.DeleteBytes(doc, "tools")
		doc, _ = sjson.DeleteBytes(doc, "functions")
		return doc
	}

	if choice.Type == gjson.String {
		if strings.EqualFold(strings.TrimSpace(choice.String()), "none") {
			return dropTools(body)
		}
		return body
	}

	if !choice.IsObject() {
		return dropTools(body)
	}

	switch strings.ToLower(strings.TrimSpace(choice.Get("type").String())) {
	case "none":
		return dropTools(body)
	case "auto", "required":
		updated, err := sjson.SetBytes(body, "tool_choice", strings.ToLower(strings.TrimSpace(choice.Get("type").String())))
		if err != nil {
			return body
		}
		return updated
	case "function":
		name := strings.TrimSpace(choice.Get("function.name").String())
		if name == "" {
			name = strings.TrimSpace(choice.Get("name").String())
		}
		if name == "" {
			name = "auto"
		}
		updated, err := sjson.SetBytes(body, "tool_choice", name)
		if err != nil {
			return body
		}
		return updated
	default:
		// 未识别的对象形式一律删除：留着必然被上游判为非法参数。
		updated, _ := sjson.DeleteBytes(body, "tool_choice")
		return updated
	}
}
