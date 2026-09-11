package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCodeBuddyCredentialsMigration 固化 TASK-002 的架构决策：CodeBuddy 复用
// accounts + credentials(jsonb)，不新建平台专属凭据表；本迁移负责存量归一化
// 与枚举约束，且必须保持幂等。
func TestCodeBuddyCredentialsMigration(t *testing.T) {
	content, err := FS.ReadFile("240_codebuddy_credentials.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	// 决策：不新建 tencent_codebuddy_accounts 等独立凭据表。
	require.NotContains(t, sql, "CREATE TABLE")

	// 1) 存量归一化：非法/缺失的 product、region 回落到 codebuddy / china。
	require.Contains(t, sql, "UPDATE accounts")
	require.Contains(t, sql, "credentials || jsonb_build_object(")
	require.Contains(t, sql, "WHERE platform = 'codebuddy'")
	require.Contains(t, sql, "COALESCE(credentials->>'product', '') NOT IN ('workbuddy', 'codebuddy')")
	require.Contains(t, sql, "COALESCE(credentials->>'region', '') NOT IN ('global', 'china')")

	// 2) 约束：幂等创建 + NOT VALID/VALIDATE（与 154_account_spark_shadow 一致）。
	require.Contains(t, sql, "SELECT 1 FROM pg_constraint WHERE conname = 'chk_accounts_codebuddy_credentials'")
	require.Contains(t, sql, "ALTER TABLE accounts ADD CONSTRAINT chk_accounts_codebuddy_credentials")
	require.Contains(t, sql, "NOT VALID")
	require.Contains(t, sql, "ALTER TABLE accounts VALIDATE CONSTRAINT chk_accounts_codebuddy_credentials")

	// NULL 安全：缺键必须判违约（CHECK 只在 FALSE 时拒绝，NULL 会被静默放过），
	// 因此校验侧必须用 COALESCE，并且对非 codebuddy 行放行。
	require.Contains(t, sql, "platform <> 'codebuddy' OR (")
	require.Contains(t, sql, "COALESCE(credentials->>'product', '') IN ('workbuddy', 'codebuddy')")
	require.Contains(t, sql, "COALESCE(credentials->>'region', '') IN ('global', 'china')")
}
