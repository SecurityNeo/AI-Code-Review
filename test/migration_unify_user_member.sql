-- =====================================================
-- CodeGuard 人员体系统一数据库迁移脚本
-- 日期: 2026-08-22
-- 原则: 数据零丢失、team_members 表保留
-- =====================================================

-- -----------------------------------------------------
-- Step 0: 备份（强制执行）
-- -----------------------------------------------------
-- 核心表备份
CREATE TABLE team_members_backup AS SELECT * FROM team_members;
CREATE TABLE users_backup AS SELECT * FROM users;
CREATE TABLE project_responsibilities_backup AS SELECT * FROM project_responsibilities;

-- mcp_call_logs 是 MCP 服务新引入的表，如未部署则跳过备份
SET @mcp_exists = (SELECT COUNT(*) FROM information_schema.tables 
                   WHERE table_schema = DATABASE() AND table_name = 'mcp_call_logs');
SET @mcp_backup_sql = IF(@mcp_exists > 0, 
                         'CREATE TABLE mcp_call_logs_backup AS SELECT * FROM mcp_call_logs',
                         'SELECT \'mcp_call_logs not found, skip backup\' AS msg');
PREPARE mcp_stmt FROM @mcp_backup_sql;
EXECUTE mcp_stmt;
DEALLOCATE PREPARE mcp_stmt;

-- -----------------------------------------------------
-- Step 1: 前置检查
-- -----------------------------------------------------
-- 1.1 确认 username 映射关系
SELECT u.id AS uid, u.username, tm.id AS tmid, tm.display_name
FROM users u
LEFT JOIN team_members tm ON u.username = tm.username COLLATE utf8mb4_unicode_ci
ORDER BY u.id;

-- 1.2 确认孤儿 team_members（只在 team_members 中，不在 users 中）
SELECT tm.id, tm.username, tm.display_name
FROM team_members tm
LEFT JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
WHERE u.id IS NULL;

-- -----------------------------------------------------
-- Step 2: 扩展 users 表
-- -----------------------------------------------------
ALTER TABLE users
    ADD COLUMN im_platform VARCHAR(20) DEFAULT 'wecom' AFTER avatar_url,
    ADD COLUMN im_user_id VARCHAR(100) AFTER im_platform,
    ADD COLUMN enabled TINYINT(1) DEFAULT 1 AFTER im_user_id,
    ADD COLUMN email VARCHAR(255) AFTER enabled;

-- 验证
DESCRIBE users;

-- -----------------------------------------------------
-- Step 3: 为孤儿 team_members 创建 users 记录
-- -----------------------------------------------------
INSERT INTO users (username, display_name, role, login_type, gitlab_username, password,
                   im_platform, im_user_id, enabled, created_at, updated_at)
SELECT username,
       display_name,
       CASE WHEN role = 'member' THEN 'user' ELSE role END,
       'local',
       gitlab_username,
       '',
       COALESCE(im_platform, 'wecom'),
       im_user_id,
       enabled,
       NOW(),
       NOW()
FROM team_members
WHERE username COLLATE utf8mb4_unicode_ci NOT IN (SELECT username COLLATE utf8mb4_unicode_ci FROM users);

-- -----------------------------------------------------
-- Step 4: 同步 users 的扩展字段（以 team_members 为源，不覆盖 display_name）
-- -----------------------------------------------------
UPDATE users u
JOIN team_members tm ON u.username = tm.username COLLATE utf8mb4_unicode_ci
SET u.im_platform = COALESCE(tm.im_platform, 'wecom'),
    u.im_user_id = tm.im_user_id,
    u.enabled = COALESCE(tm.enabled, 1),
    u.email = tm.email;

-- -----------------------------------------------------
-- Step 5: project_responsibilities 增加 user_id（双键兼容）
-- -----------------------------------------------------
ALTER TABLE project_responsibilities
    ADD COLUMN user_id BIGINT UNSIGNED AFTER member_id;

-- 通过 username 映射填充（核心映射：pr.member_id -> tm.id -> tm.username -> u.username -> u.id）
UPDATE project_responsibilities pr
JOIN team_members tm ON tm.id = pr.member_id
JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
SET pr.user_id = u.id;

-- 5.1 验证：确保无 NULL
SELECT COUNT(*) AS null_count FROM project_responsibilities WHERE user_id IS NULL;
-- 期望: 0

-- 5.2 验证：映射一致性
SELECT COUNT(*) AS mismatch
FROM project_responsibilities pr
JOIN team_members tm ON tm.id = pr.member_id
JOIN users u ON u.id = pr.user_id
WHERE tm.username COLLATE utf8mb4_unicode_ci != u.username COLLATE utf8mb4_unicode_ci;
-- 期望: 0

-- 5.3 加索引（⚠️ 注意：不加外键约束！因为 DeleteUser 是硬删除）
ALTER TABLE project_responsibilities
    ADD INDEX idx_user_id (user_id),
    ADD INDEX idx_user_project (user_id, project_id);

-- -----------------------------------------------------
-- Step 6: mcp_call_logs 增加 user_id（如表不存在则跳过，由 MCP 首次部署时 AutoMigrate 创建）
-- -----------------------------------------------------
SET @mcp_exists_2 = (SELECT COUNT(*) FROM information_schema.tables 
                     WHERE table_schema = DATABASE() AND table_name = 'mcp_call_logs');
SET @mcp_alter_sql = IF(@mcp_exists_2 > 0, 
                        'ALTER TABLE mcp_call_logs ADD COLUMN user_id BIGINT UNSIGNED AFTER team_member_id',
                        'SELECT \'mcp_call_logs not found, skip ALTER (will be created by AutoMigrate)\' AS msg');
PREPARE mcp_alter_stmt FROM @mcp_alter_sql;
EXECUTE mcp_alter_stmt;
DEALLOCATE PREPARE mcp_alter_stmt;

-- -----------------------------------------------------
-- Step 7: users 表加 IM 查询索引
-- -----------------------------------------------------
ALTER TABLE users ADD INDEX idx_users_im (im_platform, im_user_id);

-- -----------------------------------------------------
-- Step 8: 最终验证
-- -----------------------------------------------------

-- 验证1：users 新增字段已填充
SELECT COUNT(*) AS total_users,
       SUM(CASE WHEN im_platform IS NOT NULL THEN 1 ELSE 0 END) AS has_im_platform,
       SUM(CASE WHEN im_user_id IS NOT NULL THEN 1 ELSE 0 END) AS has_im_user_id,
       SUM(CASE WHEN enabled IS NOT NULL THEN 1 ELSE 0 END) AS has_enabled
FROM users;

-- 验证2：project_responsibilities user_id 无 NULL
SELECT COUNT(*) AS null_count FROM project_responsibilities WHERE user_id IS NULL;

-- 验证3：users role 无异常值
SELECT DISTINCT role FROM users;
-- 期望: admin, user

-- 验证4：users gitlab_username 无重复（如需加唯一索引）
SELECT gitlab_username, COUNT(*) AS cnt
FROM users
WHERE gitlab_username IS NOT NULL AND gitlab_username != ''
GROUP BY gitlab_username
HAVING cnt > 1;
-- 期望: 空集

-- 验证5：project_responsibilities 索引
SHOW INDEX FROM project_responsibilities;

-- 验证6：users 索引
SHOW INDEX FROM users;

-- -----------------------------------------------------
-- Step 9: 验证 user_id 回填率（方案 B 前置检查）
-- -----------------------------------------------------
-- 9.1 检查是否存在 user_id = 0 或 NULL 的记录
SELECT COUNT(*) AS orphan_count
FROM project_responsibilities
WHERE user_id IS NULL OR user_id = 0;
-- 期望: 0（若不为0，必须先执行 9.2 清理）

-- 9.1b 严格验证：确保 user_id > 0 的记录占比 100%
SELECT 
    COUNT(*) AS total,
    SUM(CASE WHEN user_id > 0 THEN 1 ELSE 0 END) AS valid_count,
    SUM(CASE WHEN user_id IS NULL OR user_id = 0 THEN 1 ELSE 0 END) AS orphan_count
FROM project_responsibilities;
-- 期望: total = valid_count, orphan_count = 0
-- 若 orphan_count > 0，必须执行 9.2 后再重新验证

-- 9.2 为缺失 user_id 的孤儿 team_members 创建 users 记录
INSERT INTO users (username, display_name, role, login_type, password,
                   im_platform, im_user_id, enabled, created_at, updated_at)
SELECT tm.username,
       COALESCE(tm.display_name, tm.username),
       CASE WHEN tm.role = 'member' THEN 'user' ELSE tm.role END,
       'local',
       '',
       COALESCE(tm.im_platform, 'wecom'),
       tm.im_user_id,
       COALESCE(tm.enabled, 1),
       NOW(),
       NOW()
FROM team_members tm
JOIN project_responsibilities pr ON pr.member_id = tm.id
LEFT JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
WHERE (pr.user_id IS NULL OR pr.user_id = 0)
  AND u.id IS NULL;

-- 再次回填 user_id
UPDATE project_responsibilities pr
JOIN team_members tm ON tm.id = pr.member_id
JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
SET pr.user_id = u.id
WHERE pr.user_id IS NULL OR pr.user_id = 0;

-- -----------------------------------------------------
-- Step 9.6: 修复 review_issues.current_owner_id 遗留数据
-- 背景: cb787ba (2026-07-28) 人员模型重构时，
--       current_owner_id 被错误地从 users.id 改为了 team_members.id。
--       此步骤仅修复那段时期产生的遗留值。
-- -----------------------------------------------------
-- 9.6.1 统计受影响的记录数
SELECT COUNT(*) AS affected_count
FROM review_issues ri
JOIN team_members tm ON tm.id = ri.current_owner_id
JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
WHERE ri.current_owner_id IS NOT NULL
  AND ri.current_owner_id > 0
  AND ri.current_owner_id != u.id;

-- 9.6.2 执行修复（仅当 current_owner_id 能映射到 team_members，
-- 且对应的 users.id 与当前值不一致时才更新）
UPDATE review_issues ri
JOIN team_members tm ON tm.id = ri.current_owner_id
JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
SET ri.current_owner_id = u.id
WHERE ri.current_owner_id IS NOT NULL
  AND ri.current_owner_id > 0
  AND ri.current_owner_id != u.id;

-- 9.6.3 验证修复后应无残留
SELECT COUNT(*) AS remaining
FROM review_issues ri
JOIN team_members tm ON tm.id = ri.current_owner_id
JOIN users u ON u.username = tm.username COLLATE utf8mb4_unicode_ci
WHERE ri.current_owner_id IS NOT NULL
  AND ri.current_owner_id > 0
  AND ri.current_owner_id != u.id;
-- 期望: 0

-- -----------------------------------------------------
-- Step 10: 重建唯一索引（方案 B 核心：user_id 取代 member_id）
-- -----------------------------------------------------
-- 10.1 预检查：user_id 维度是否存在重复
SELECT user_id, project_id, scope_type, scope_value, COUNT(*) as cnt
FROM project_responsibilities
GROUP BY user_id, project_id, scope_type, scope_value
HAVING cnt > 1;
-- 期望: 空集。若有重复，需人工处理后才能继续

-- 10.2 删除旧唯一索引
ALTER TABLE project_responsibilities DROP INDEX uk_member_project_scope;

-- 10.3 创建新唯一索引（基于 user_id）
ALTER TABLE project_responsibilities
    ADD UNIQUE INDEX uk_user_project_scope(user_id, project_id, scope_type, scope_value);

-- 10.4 member_id 改为 nullable（允许未来完全退役该字段）
ALTER TABLE project_responsibilities MODIFY member_id BIGINT UNSIGNED NULL;

-- 10.5 （可选）删除 member_id 普通索引
ALTER TABLE project_responsibilities DROP INDEX idx_member_project;

-- -----------------------------------------------------
-- Step 11: 最终验证（方案 B）
-- -----------------------------------------------------
-- 11.1 唯一索引生效验证
SHOW INDEX FROM project_responsibilities;
-- 期望: uk_user_project_scope 存在，uk_member_project_scope 已消失

-- 11.2 统计新旧记录分布
SELECT
    COUNT(*) AS total,
    SUM(CASE WHEN user_id > 0 THEN 1 ELSE 0 END) AS has_user_id,
    SUM(CASE WHEN member_id IS NOT NULL THEN 1 ELSE 0 END) AS has_member_id,
    SUM(CASE WHEN user_id > 0 AND member_id IS NULL THEN 1 ELSE 0 END) AS user_only,
    SUM(CASE WHEN user_id = 0 OR user_id IS NULL THEN 1 ELSE 0 END) AS orphan
FROM project_responsibilities;
-- 期望: has_user_id = total, orphan = 0
