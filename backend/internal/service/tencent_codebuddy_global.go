package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// WorkBuddy 国际版（www.workbuddy.ai）新账号的注册激活与 trial 加油包，移植自参考实现
// workbuddy2api-panel internal/upstream 的 global_register.go / trial.go。
//
// 新国际版账号登录后要先补注册地区、再调 register 激活 Trial，否则对话报
// 14017 trial not activated。链路（逆向自 web 注册完善页）：
//
//	GET  /auth/realms/copilot/overseas/user/register?userId=<uid> → code 200 已激活；500 / "region required" 需补地区
//	POST /billing/area/get-country-code {filterForbidden:1}     → 可选国家（data 可能是 JSON 字符串）
//	POST /console/login/account {attributes:{...}}               → 提交地区（幂等）
//	POST /billing/ide/trial                                      → 一次性加油包（14051 = 已领过）

// tencentWorkBuddyGlobalWebUA 是国际版 web 端 UA：注册完善页走 web 指纹，不是桌面端指纹。
const tencentWorkBuddyGlobalWebUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// tencentWorkBuddyGlobalRegionWhitelist 是国际版 web 展示的可选地区，取第一个可用的提交。
var tencentWorkBuddyGlobalRegionWhitelist = []string{"HK", "MO", "SG", "TH", "PH", "MY", "ID"}

// isTencentWorkBuddyGlobal 报告凭据是否属于 WorkBuddy 国际版。
func isTencentWorkBuddyGlobal(cred TencentCodeBuddyCredential) bool {
	endpoint := ResolveTencentCodeBuddyEndpoint(cred.Product, cred.Region)
	return endpoint.Product == TencentCodeBuddyProductWorkBuddy && endpoint.Region == TencentCodeBuddyRegionGlobal
}

// globalCall 发一次国际版 web 接口请求，返回信封的 code / msg / data（不把非 0 code 当错误）。
func (c *TencentCodeBuddyClient) globalCall(ctx context.Context, account *Account, method, path string, payload any) (int64, string, json.RawMessage, error) {
	var body []byte
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, "", nil, err
		}
		body = raw
	}
	resp, err := c.do(ctx, account, account.TencentCodeBuddyCredential(), method, tencentWorkBuddyAPIHostIntl+path, body, false,
		map[string]string{
			"User-Agent": tencentWorkBuddyGlobalWebUA,
			"Accept":     "application/json, text/plain, */*",
		})
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0, "", nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateString(strings.TrimSpace(string(raw)), 200))
	}
	code, _ := envelope.Code.Int64()
	return code, envelope.Msg, envelope.Data, nil
}

// globalRegisterStatus 查询注册激活状态。
func (c *TencentCodeBuddyClient) globalRegisterStatus(ctx context.Context, account *Account) (activated, needsRegion bool, msg string, err error) {
	uid := account.TencentCodeBuddyCredential().UserID
	code, msg, _, err := c.globalCall(ctx, account, http.MethodGet, "/auth/realms/copilot/overseas/user/register?userId="+uid, nil)
	if err != nil {
		return false, false, "", err
	}
	switch {
	case code == 200:
		return true, false, msg, nil
	case code == 500 || strings.Contains(strings.ToLower(msg), "region required"):
		return false, true, msg, nil
	}
	return false, false, msg, nil
}

type tencentWorkBuddyGlobalCountry struct {
	EnName string `json:"EnName"`
	IOS2   string `json:"IOS2"`
	Code   string `json:"Code"`
}

// globalPickCountry 取白名单里第一个可选的国家（通常是 HK）。
func (c *TencentCodeBuddyClient) globalPickCountry(ctx context.Context, account *Account) (*tencentWorkBuddyGlobalCountry, error) {
	code, msg, data, err := c.globalCall(ctx, account, http.MethodPost, "/billing/area/get-country-code", map[string]any{"filterForbidden": 1})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("get-country-code: %s (code=%d)", msg, code)
	}
	// data 可能是 JSON 字符串（双层信封）。
	if s := strings.TrimSpace(string(data)); strings.HasPrefix(s, `"`) {
		var inner string
		if err := json.Unmarshal(data, &inner); err != nil {
			return nil, err
		}
		data = json.RawMessage(inner)
	}
	var list struct {
		Data struct {
			List []tencentWorkBuddyGlobalCountry `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse country list: %w", err)
	}
	for _, want := range tencentWorkBuddyGlobalRegionWhitelist {
		for i := range list.Data.List {
			if list.Data.List[i].IOS2 == want {
				return &list.Data.List[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no available region")
}

// ActivateWorkBuddyGlobal 完成国际版账号的注册激活（幂等：已激活直接返回），
// 需要时自动补注册地区；随后领取一次性 trial 加油包（已领过不算失败）。
// 返回给管理员看的一句话结果。
func (c *TencentCodeBuddyClient) ActivateWorkBuddyGlobal(ctx context.Context, account *Account) (string, error) {
	if account == nil || !isTencentWorkBuddyGlobal(account.TencentCodeBuddyCredential()) {
		return "", fmt.Errorf("only WorkBuddy global accounts")
	}
	activated, needsRegion, msg, err := c.globalRegisterStatus(ctx, account)
	if err != nil {
		return "", fmt.Errorf("查询注册状态失败：%w", err)
	}
	result := "账号已激活"
	if !activated {
		if !needsRegion {
			return "", fmt.Errorf("注册未激活：%s", msg)
		}
		country, err := c.globalPickCountry(ctx, account)
		if err != nil {
			return "", fmt.Errorf("获取注册地区失败：%w", err)
		}
		code, msg, _, err := c.globalCall(ctx, account, http.MethodPost, "/console/login/account", map[string]any{
			"attributes": map[string]any{
				"countryCode":     []string{country.Code},
				"countryFullName": []string{country.EnName},
				"countryName":     []string{country.IOS2},
			},
		})
		if err != nil {
			return "", fmt.Errorf("提交注册地区失败：%w", err)
		}
		if code != 0 {
			return "", fmt.Errorf("提交注册地区失败：%s (code=%d)", msg, code)
		}
		if activated, _, msg, err = c.globalRegisterStatus(ctx, account); err != nil || !activated {
			if err == nil {
				err = fmt.Errorf("%s", msg)
			}
			return "", fmt.Errorf("补地区（%s）后仍未激活：%w", country.IOS2, err)
		}
		result = "已补注册地区 " + country.IOS2 + " 并激活"
	}
	code, msg, _, err := c.globalCall(ctx, account, http.MethodPost, "/billing/ide/trial", nil)
	switch {
	case err != nil:
		result += "；trial 加油包领取失败：" + err.Error()
	case code == 0:
		result += "；已领取 trial 加油包"
	case code == 14051:
		// 已领过，幂等成功。
	default:
		result += fmt.Sprintf("；trial 加油包领取失败：%s (code=%d)", msg, code)
	}
	return result, nil
}

// ActivateWorkBuddyGlobalCredential 用登录拿到的凭据（尚未建账号）执行国际版激活。
func (p *TencentCodeBuddyProvider) ActivateWorkBuddyGlobalCredential(ctx context.Context, cred TencentCodeBuddyCredential) (string, error) {
	if p == nil || p.client == nil {
		return "", fmt.Errorf("tencent codebuddy provider is not configured")
	}
	account := &Account{
		Platform:    PlatformTencentCodeBuddy,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			tencentCodeBuddyCredAccessToken: cred.AccessToken,
			tencentCodeBuddyCredProduct:     cred.Product,
			tencentCodeBuddyCredRegion:      cred.Region,
			tencentCodeBuddyCredUserID:      cred.UserID,
		},
	}
	return p.client.ActivateWorkBuddyGlobal(ctx, account)
}
