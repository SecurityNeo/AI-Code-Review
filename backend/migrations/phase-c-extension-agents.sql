-- Phase C 扩展智能体数据库迁移脚本
-- 执行时间: 2026-08-10

-- ========== 1. review_agent_configs 表扩展列 ==========
ALTER TABLE review_agent_configs
    ADD COLUMN IF NOT EXISTS secret_scan_enabled     TINYINT(1) NOT NULL DEFAULT 0 COMMENT '密钥扫描智能体开关',
    ADD COLUMN IF NOT EXISTS security_audit_enabled  TINYINT(1) NOT NULL DEFAULT 0 COMMENT '安全审计智能体开关',
    ADD COLUMN IF NOT EXISTS test_suggestion_enabled TINYINT(1) NOT NULL DEFAULT 0 COMMENT '测试建议智能体开关',
    ADD COLUMN IF NOT EXISTS impact_analysis_enabled TINYINT(1) NOT NULL DEFAULT 0 COMMENT '影响分析智能体开关',
    ADD COLUMN IF NOT EXISTS license_check_enabled   TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'License检查智能体开关';

-- ========== 2. secret_scan_rules 密钥扫描规则库 ==========
CREATE TABLE IF NOT EXISTS secret_scan_rules (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    code        VARCHAR(64)  NOT NULL COMMENT '规则编码，如 SECRET-001',
    name        VARCHAR(128) NOT NULL COMMENT '规则名称',
    category    VARCHAR(32)  NOT NULL COMMENT '分类: aws/github/generic/database/private_key',
    severity    VARCHAR(16)  NOT NULL COMMENT 'critical/high/medium',
    pattern     TEXT         NOT NULL COMMENT '正则表达式（RE2语法）',
    keywords    TEXT         NULL COMMENT '关键词触发（逗号分隔，预过滤用）',
    description TEXT         NULL COMMENT '规则描述',
    is_builtin  TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '是否系统内置',
    enabled     TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '是否启用',
    created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_code (code),
    KEY idx_category (category),
    KEY idx_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='密钥扫描规则库';

-- ========== 3. secret_scan_findings 密钥扫描发现记录 ==========
CREATE TABLE IF NOT EXISTS secret_scan_findings (
    id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id      BIGINT UNSIGNED NOT NULL COMMENT '关联任务ID',
    rule_code    VARCHAR(64)  NOT NULL COMMENT '规则编码',
    file_path    VARCHAR(512) NOT NULL COMMENT '文件路径',
    line_number  INT          NOT NULL COMMENT '行号',
    match_text   TEXT         NULL COMMENT '匹配到的原始文本（脱敏存储）',
    severity     VARCHAR(16)  NULL COMMENT '严重级别',
    description  TEXT         NULL COMMENT '规则描述快照',
    is_confirmed TINYINT(1)   NOT NULL DEFAULT 0 COMMENT '管理员是否已确认（白名单预留）',
    created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_task_id (task_id),
    KEY idx_rule_code (rule_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='密钥扫描发现记录';

-- ========== 4. security_audit_rules 安全审计规则库 ==========
CREATE TABLE IF NOT EXISTS security_audit_rules (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    code          VARCHAR(64)  NOT NULL COMMENT '规则编码，如 SA_SQL_INJECTION',
    name          VARCHAR(128) NOT NULL COMMENT '规则名称',
    category      VARCHAR(32)  NOT NULL COMMENT '分类: sql_injection/unsafe_eval/deserialization',
    severity      VARCHAR(16)  NOT NULL COMMENT 'critical/high/medium/low',
    language      VARCHAR(32)  NULL COMMENT '语言（空=通用，Go/Java/Python/JS）',
    mode          VARCHAR(16)  NOT NULL COMMENT 'ast / regex',
    ast_pattern   TEXT         NULL COMMENT 'AST DSL（Mode=ast时使用）',
    regex_pattern TEXT         NULL COMMENT '正则表达式（Mode=regex时使用）',
    description   TEXT         NULL COMMENT '规则描述',
    suggestion    TEXT         NULL COMMENT '修复建议',
    is_builtin    TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '是否系统内置',
    enabled       TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '是否启用',
    created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_code (code),
    KEY idx_category (category),
    KEY idx_language (language),
    KEY idx_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='安全审计规则库';

-- ========== 5. security_audit_findings 安全审计发现记录 ==========
CREATE TABLE IF NOT EXISTS security_audit_findings (
    id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id      BIGINT UNSIGNED NOT NULL COMMENT '关联任务ID',
    rule_code    VARCHAR(64)  NOT NULL COMMENT '规则编码',
    file_path    VARCHAR(512) NOT NULL COMMENT '文件路径',
    line_number  INT          NOT NULL COMMENT '行号',
    column_start INT          NULL COMMENT '列起始位置',
    column_end   INT          NULL COMMENT '列结束位置',
    snippet      TEXT         NULL COMMENT '代码片段',
    severity     VARCHAR(16)  NULL COMMENT '严重级别',
    message      TEXT         NULL COMMENT '发现问题描述',
    suggestion   TEXT         NULL COMMENT '修复建议（快照）',
    created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_task_id (task_id),
    KEY idx_rule_code (rule_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='安全审计发现记录';

-- ========== 6. License检查内置规则（无需独立表，逻辑内置）==========
-- LicenseCheck 使用内置兼容矩阵，不存储用户规则
