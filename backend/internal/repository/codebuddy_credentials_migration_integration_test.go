//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// TestMigration240CodeBuddyCredentialsContract 在真实 PostgreSQL 上验证 240 迁移：
//  1. 幂等：同一份 SQL 连续执行两次都成功；
//  2. 存量归一化：缺失/非法的 product、region 被回落到 codebuddy / china，
//     已合法的值保持不变；
//  3. 约束生效：非法枚举与缺键的 codebuddy 行被拒绝，非 codebuddy 行不受影响。
func TestMigration240CodeBuddyCredentialsContract(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	migrationSQL, err := dbmigrations.FS.ReadFile("240_codebuddy_credentials.sql")
	require.NoError(t, err)

	// 基线 harness 已应用全部迁移，这里先移除约束，以覆盖迁移自身的创建路径。
	_, err = tx.ExecContext(ctx, `ALTER TABLE accounts DROP CONSTRAINT IF EXISTS chk_accounts_codebuddy_credentials`)
	require.NoError(t, err)

	insertAccount := func(name, platform, credentials string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, credentials)
VALUES ($1, $2, 'apikey', $3::jsonb)
RETURNING id
`, name, platform, credentials).Scan(&id))
		return id
	}

	missingID := insertAccount("migration-240-missing", "codebuddy", `{"access_token":"tok-missing"}`)
	invalidID := insertAccount("migration-240-invalid", "codebuddy", `{"access_token":"tok-invalid","product":"bogus","region":"mars"}`)
	validID := insertAccount("migration-240-valid", "codebuddy", `{"access_token":"tok-valid","product":"workbuddy","region":"global"}`)
	otherID := insertAccount("migration-240-other", "openai", `{"product":"bogus","region":"mars"}`)

	// 幂等：连跑两次都必须成功。
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	credentialsOf := func(id int64) (string, string) {
		t.Helper()
		var product, region string
		require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COALESCE(credentials->>'product', ''), COALESCE(credentials->>'region', '')
FROM accounts WHERE id = $1
`, id).Scan(&product, &region))
		return product, region
	}

	// 缺失键 -> 默认值。
	product, region := credentialsOf(missingID)
	require.Equal(t, "codebuddy", product)
	require.Equal(t, "china", region)

	// 非法值 -> 默认值。
	product, region = credentialsOf(invalidID)
	require.Equal(t, "codebuddy", product)
	require.Equal(t, "china", region)

	// 合法值 -> 保持不变。
	product, region = credentialsOf(validID)
	require.Equal(t, "workbuddy", product)
	require.Equal(t, "global", region)

	// 非 codebuddy 行 -> 完全不受影响。
	product, region = credentialsOf(otherID)
	require.Equal(t, "bogus", product)
	require.Equal(t, "mars", region)

	// 约束拒绝路径必须在 SAVEPOINT 内断言，否则语句错误会中止整个事务。
	requireRejected := func(credentials string) {
		t.Helper()
		_, err := tx.ExecContext(ctx, `SAVEPOINT migration_240_reject`)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
INSERT INTO accounts (name, platform, type, credentials)
VALUES ('migration-240-reject', 'codebuddy', 'apikey', $1::jsonb)
`, credentials)
		require.Error(t, err, "expected CHECK constraint to reject %s", credentials)
		_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT migration_240_reject`)
		require.NoError(t, rollbackErr)
	}

	requireRejected(`{"access_token":"tok-x","product":"bogus","region":"china"}`)
	requireRejected(`{"access_token":"tok-x","product":"codebuddy","region":"mars"}`)
	requireRejected(`{"access_token":"tok-x"}`)
	requireRejected(`{"access_token":"tok-x","product":"codebuddy"}`)

	// 合法写入仍然放行。
	_, err = tx.ExecContext(ctx, `
INSERT INTO accounts (name, platform, type, credentials)
VALUES ('migration-240-accept', 'codebuddy', 'apikey',
        '{"access_token":"tok-y","product":"codebuddy","region":"china"}'::jsonb)
`)
	require.NoError(t, err)
}
