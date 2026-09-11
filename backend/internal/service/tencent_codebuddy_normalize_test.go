//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// ===== 增量归一化（批量 JSONB 顶层合并） =====

func TestHasTencentCodeBuddyCredentialKeys(t *testing.T) {
	require.False(t, HasTencentCodeBuddyCredentialKeys(nil))
	require.False(t, HasTencentCodeBuddyCredentialKeys(map[string]any{}))
	// base_url 是多平台共用键，单独出现不构成 CodeBuddy 更新意图。
	require.False(t, HasTencentCodeBuddyCredentialKeys(map[string]any{"base_url": "https://example.com"}))
	require.False(t, HasTencentCodeBuddyCredentialKeys(map[string]any{"api_key": "sk-1"}))
	for _, key := range tencentCodeBuddyCredentialKeys {
		require.True(t, HasTencentCodeBuddyCredentialKeys(map[string]any{key: "x"}), key)
	}
}

func TestNormalizeTencentCodeBuddyCredentialUpdate_OnlyRewritesIncrementKeys(t *testing.T) {
	base := map[string]any{
		tencentCodeBuddyCredAccessToken: "at-existing",
		tencentCodeBuddyCredProduct:     "workbuddy",
		tencentCodeBuddyCredRegion:      "global",
	}
	increment := map[string]any{tencentCodeBuddyCredRegion: "mars"}

	require.NoError(t, NormalizeTencentCodeBuddyCredentialUpdate(AccountTypeAPIKey, increment, base))

	require.Equal(t, "china", increment[tencentCodeBuddyCredRegion])
	// 未被增量覆盖的字段不得写进共享增量，否则会把 A 账号的 product 广播给 B 账号。
	_, hasProduct := increment[tencentCodeBuddyCredProduct]
	require.False(t, hasProduct)
	// base 是目标账号既有凭据的只读参照，不得被就地改写。
	require.Equal(t, "workbuddy", base[tencentCodeBuddyCredProduct])
	require.Equal(t, "global", base[tencentCodeBuddyCredRegion])
}

func TestNormalizeTencentCodeBuddyCredentialUpdate_ResolvesAccessTokenFromBase(t *testing.T) {
	// 增量不含 access_token 时由目标账号既有凭据补全（批量合并语义）。
	increment := map[string]any{tencentCodeBuddyCredProduct: "bogus"}
	require.NoError(t, NormalizeTencentCodeBuddyCredentialUpdate(AccountTypeAPIKey, increment, map[string]any{
		tencentCodeBuddyCredAccessToken: "at-existing",
	}))
	require.Equal(t, "codebuddy", increment[tencentCodeBuddyCredProduct])

	// 目标账号本身缺 access_token 且增量也未提供 -> 拒绝，避免把不合规账号写坏。
	err := NormalizeTencentCodeBuddyCredentialUpdate(AccountTypeAPIKey, map[string]any{
		tencentCodeBuddyCredProduct: "codebuddy",
	}, map[string]any{})
	requireApplicationErrorReason(t, err, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN")
}

func TestNormalizeTencentCodeBuddyCredentialUpdate_DropsBaseURL(t *testing.T) {
	increment := map[string]any{
		tencentCodeBuddyCredAccessToken: "at-new",
		"base_url":                      "https://attacker.example.com",
	}
	require.NoError(t, NormalizeTencentCodeBuddyCredentialUpdate(AccountTypeAPIKey, increment, nil))
	_, has := increment["base_url"]
	require.False(t, has, "endpoint 由 product + region 固定，批量增量也不得写入 base_url")
}

// ===== 路径 1：UpdateAccount（单账号编辑 / BatchUpdateCredentials 循环 / OAuth 应用） =====

func TestAdminServiceUpdateAccount_NormalizesTencentCodeBuddyCredentials(t *testing.T) {
	account := &Account{
		ID:       7,
		Platform: PlatformTencentCodeBuddy,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			tencentCodeBuddyCredAccessToken: "at-existing",
			tencentCodeBuddyCredProduct:     "workbuddy",
			tencentCodeBuddyCredRegion:      "global",
		},
	}
	repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{7: account}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{
		Credentials: map[string]any{
			tencentCodeBuddyCredRegion: "mars",
			tencentCodeBuddyCredUserID: "  uid-1  ",
			"base_url":                 "https://attacker.example.com",
		},
	})
	require.NoError(t, err)

	// 非法 region 回落默认；uid 去空白；access_token 作为敏感键被保留。
	require.Equal(t, "china", updated.Credentials[tencentCodeBuddyCredRegion])
	require.Equal(t, "uid-1", updated.Credentials[tencentCodeBuddyCredUserID])
	require.Equal(t, "at-existing", updated.Credentials[tencentCodeBuddyCredAccessToken])
	// product 是非敏感键且增量未携带，按全对象 PUT 语义被丢弃后由归一化补默认值。
	require.Equal(t, "codebuddy", updated.Credentials[tencentCodeBuddyCredProduct])
	_, hasBaseURL := updated.Credentials["base_url"]
	require.False(t, hasBaseURL, "CodeBuddy endpoint 固定，编辑路径必须清掉 base_url")
	require.Len(t, repo.updatedAccounts, 1)
}

func TestAdminServiceUpdateAccount_RejectsTencentCodeBuddyBlankAccessToken(t *testing.T) {
	account := &Account{
		ID:          7,
		Platform:    PlatformTencentCodeBuddy,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{tencentCodeBuddyCredAccessToken: "at-existing"},
	}
	repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{7: account}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{
		Credentials: map[string]any{tencentCodeBuddyCredAccessToken: "   "},
	})
	requireApplicationErrorReason(t, err, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN")
	require.Empty(t, repo.updatedAccounts, "校验必须在写库之前失败")
}

func TestAdminServiceUpdateAccount_RejectsTencentCodeBuddyTypeChange(t *testing.T) {
	account := &Account{
		ID:          7,
		Platform:    PlatformTencentCodeBuddy,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{tencentCodeBuddyCredAccessToken: "at-existing"},
	}
	repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{7: account}}
	svc := &adminServiceImpl{accountRepo: repo}

	// 只改类型、不传凭据，也必须被唯一入口拦下：apikey 是 CodeBuddy 的硬不变量。
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Type: AccountTypeOAuth})
	requireApplicationErrorReason(t, err, "TENCENT_CODEBUDDY_INVALID_ACCOUNT_TYPE")
	require.Empty(t, repo.updatedAccounts)
}

func TestAdminServiceUpdateAccount_LeavesOtherPlatformsUntouched(t *testing.T) {
	account := &Account{
		ID:          9,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "at-old"},
	}
	repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{9: account}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), 9, &UpdateAccountInput{
		Credentials: map[string]any{"access_token": "at-new", "base_url": "https://proxy.example.com"},
	})
	require.NoError(t, err)
	require.Equal(t, "at-new", updated.Credentials["access_token"])
	require.Equal(t, "https://proxy.example.com", updated.Credentials["base_url"],
		"CodeBuddy 的 base_url 清理规则不得外溢到其它平台")
}

// ===== 路径 2/3：BulkUpdateAccounts（管理后台批量更新） =====

func TestAdminServiceBulkUpdateAccounts_NormalizesTencentCodeBuddyIncrement(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{
			ID: 1, Platform: PlatformTencentCodeBuddy, Type: AccountTypeAPIKey,
			Credentials: map[string]any{
				tencentCodeBuddyCredAccessToken: "at-1",
				tencentCodeBuddyCredProduct:     "workbuddy",
				tencentCodeBuddyCredRegion:      "global",
			},
		},
		{
			ID: 2, Platform: PlatformTencentCodeBuddy, Type: AccountTypeAPIKey,
			Credentials: map[string]any{
				tencentCodeBuddyCredAccessToken: "at-2",
				tencentCodeBuddyCredProduct:     "codebuddy",
				tencentCodeBuddyCredRegion:      "china",
			},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:  []int64{1, 2},
		Credentials: map[string]any{tencentCodeBuddyCredRegion: "mars"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Success)
	require.Equal(t, 1, repo.bulkUpdateCalls)
	// 非法 region 在落库前已回落，不会撞上 240 迁移的 CHECK 约束。
	require.Equal(t, "china", repo.lastBulkUpdate.Credentials[tencentCodeBuddyCredRegion])
	// 两个目标 product 不同，共享增量不得被某个目标的既有值污染。
	_, hasProduct := repo.lastBulkUpdate.Credentials[tencentCodeBuddyCredProduct]
	require.False(t, hasProduct)
}

func TestAdminServiceBulkUpdateAccounts_RejectsTencentCodeBuddyTargetWithoutAccessToken(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformTencentCodeBuddy, Type: AccountTypeAPIKey, Credentials: map[string]any{}},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:  []int64{1},
		Credentials: map[string]any{tencentCodeBuddyCredProduct: "codebuddy"},
	})
	require.Nil(t, result)
	requireApplicationErrorReason(t, err, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN")
	require.Zero(t, repo.bulkUpdateCalls, "校验必须在批量写之前失败")
}

func TestAdminServiceBulkUpdateAccounts_IgnoresUnrelatedIncrementsForCodeBuddyTargets(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformTencentCodeBuddy, Type: AccountTypeAPIKey, Credentials: map[string]any{}},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	// 与 CodeBuddy 无关的增量（api_key）不触发 CodeBuddy 校验，避免无关批量操作被卷入。
	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:  []int64{1},
		Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Equal(t, 1, repo.bulkUpdateCalls)
}
