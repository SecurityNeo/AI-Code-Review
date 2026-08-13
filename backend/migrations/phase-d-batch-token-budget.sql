-- 分批评审 Token 配额修复 - 数据库迁移
-- 创建开销校准记录表 + 扩展 task_pipeline_executions

-- 1. 创建 overhead_calibrations 表
CREATE TABLE IF NOT EXISTS overhead_calibrations (
    id              BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    task_id         BIGINT UNSIGNED NOT NULL COMMENT '关联任务ID',
    execution_id    BIGINT UNSIGNED NOT NULL COMMENT 'Pipeline执行ID',
    stage_code      VARCHAR(50) NOT NULL COMMENT '阶段编码(batch_review_1等)',
    rule_count      INT NOT NULL DEFAULT 0 COMMENT '参与评审的规则数量',
    estimated_overhead INT NOT NULL DEFAULT 0 COMMENT '估算的系统开销(token)',
    actual_overhead    INT NOT NULL DEFAULT 0 COMMENT '实际的系统开销(token)',
    total_input_tokens INT NOT NULL DEFAULT 0 COMMENT 'LLM总输入token',
    diff_tokens        INT NOT NULL DEFAULT 0 COMMENT 'diff内容估算token',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_task (task_id),
    INDEX idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Token开销校准记录表';

-- 2. 扩展 task_pipeline_executions 表
ALTER TABLE task_pipeline_executions 
ADD COLUMN IF NOT EXISTS estimated_overhead    INT DEFAULT 0 COMMENT '估算系统开销token',
ADD COLUMN IF NOT EXISTS actual_overhead       INT DEFAULT 0 COMMENT '实际系统开销token',
ADD COLUMN IF NOT EXISTS llm_input_budget      INT DEFAULT 0 COMMENT '用户配置的LLM输入预算',
ADD COLUMN IF NOT EXISTS effective_budget      INT DEFAULT 0 COMMENT '自动扩大后的实际预算',
ADD COLUMN IF NOT EXISTS budget_warning        VARCHAR(255) DEFAULT '' COMMENT '预算警告信息';
