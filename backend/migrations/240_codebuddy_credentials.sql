-- 把腾讯 CodeBuddy / WorkBuddy 的凭据契约固化到 accounts.credentials。
--
-- 架构决策（TASK-002，方案 A）：Sub2API 不为任何平台建独立凭据表，所有平台
-- 共用 accounts 表 + credentials(jsonb)。CodeBuddy 需要的字段落点如下：
--
--   id           -> accounts.id
--   status       -> accounts.status
--   product      -> accounts.credentials->>'product'   (workbuddy / codebuddy)
--   region       -> accounts.credentials->>'region'    (global / china)
--   access_token -> accounts.credentials->>'access_token'
--   refresh_token-> accounts.credentials->>'refresh_token'
--   expires_at   -> accounts.credentials->>'expires_at'
--
-- 调度器（scheduler_snapshot_service）、Token 刷新（token_refresh_service）与
-- 网关转发（openai_gateway_cc_pipeline）都只读 accounts 表，因此新建
-- tencent_codebuddy_accounts 会产生第二份真相并使账号无法被调度/刷新。
--
-- 本迁移只做两件事：
--   1. 存量归一化：为 platform='codebuddy' 的行补齐/修正 product、region；
--   2. 新增 CHECK 约束，保证 codebuddy 行的 product / region 永远落在枚举内。
--
-- access_token / refresh_token / expires_at 是无 DB 语义的不透明数据，
-- 由应用层 NormalizeTencentCodeBuddyCredentials 在建号时校验（access_token 必填、
-- product/region 归一化写回），此处不加约束，避免把凭据完整性规则固化进
-- accounts 热表的写入路径。
--
-- 幂等性：UPDATE 只命中缺失/非法行；约束创建有 pg_constraint 守卫；
-- 与 154_account_spark_shadow.sql 一致采用 NOT VALID + VALIDATE 以避免
-- 对 accounts 全表加 ACCESS EXCLUSIVE 长锁。

-- 1) 存量归一化（幂等）
UPDATE accounts
SET credentials = credentials || jsonb_build_object(
        'product',
        CASE
            WHEN COALESCE(credentials->>'product', '') IN ('workbuddy', 'codebuddy')
                THEN credentials->>'product'
            ELSE 'codebuddy'
        END,
        'region',
        CASE
            WHEN COALESCE(credentials->>'region', '') IN ('global', 'china')
                THEN credentials->>'region'
            ELSE 'china'
        END
    )
WHERE platform = 'codebuddy'
  AND (
        COALESCE(credentials->>'product', '') NOT IN ('workbuddy', 'codebuddy')
        OR COALESCE(credentials->>'region', '') NOT IN ('global', 'china')
      );

-- 2) 约束：codebuddy 行的 product / region 必须是合法枚举。
--    注意必须用 COALESCE 包一层：CHECK 只在表达式为 FALSE 时拒绝，缺键会让
--    credentials->>'product' 求值为 NULL，NULL 参与比较得到 NULL 而非 FALSE，
--    约束会被静默放过。
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_accounts_codebuddy_credentials'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT chk_accounts_codebuddy_credentials
            CHECK (
                platform <> 'codebuddy'
                OR (
                    COALESCE(credentials->>'product', '') IN ('workbuddy', 'codebuddy')
                    AND COALESCE(credentials->>'region', '') IN ('global', 'china')
                )
            ) NOT VALID;
    END IF;
END $$;

ALTER TABLE accounts VALIDATE CONSTRAINT chk_accounts_codebuddy_credentials;
