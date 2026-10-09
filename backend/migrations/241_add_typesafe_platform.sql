-- Add TypeSafe (Jev System One) as a first-class platform.
--
-- 1. user_platform_quotas.platform CHECK
-- 2. composite_model_routes.target_platform CHECK
--
-- TypeSafe 不是对话模型，不进入渠道监控 provider，因此 channel_monitors /
-- channel_monitor_request_templates 的约束保持不变。
--
-- Runs after 238_opencode_go_platform.sql. DROP ... IF EXISTS 保证可重入；
-- 新约束是 238 的超集，存量行瞬时校验通过。
--
-- [fork] 本仓库在 239_codebuddy_platform.sql 已登记 codebuddy，这里的约束必须同时
-- 保留 codebuddy，否则存量 codebuddy 配额 / 组合路由行会让本迁移校验失败、服务无法启动
-- （242 随后会整体删除这两个 CHECK）。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'typesafe', 'codebuddy'));

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;

ALTER TABLE composite_model_routes
    ADD CONSTRAINT composite_model_routes_target_platform_check
    CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                               'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'typesafe', 'codebuddy'));
