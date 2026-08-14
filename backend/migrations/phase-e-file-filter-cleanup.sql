-- 删除已废弃的 risk_analysis 阶段定义
-- 该阶段已在代码中移除，保留数据库定义会导致 Pipeline 执行时产生无效日志
DELETE FROM review_pipeline_stages WHERE code = 'risk_analysis';

-- 放宽 SECRET-010 API Key 通用规则的引号要求
-- 变更：引号从强制变为可选，字符集加入点号，支持无引号赋值如 "apikey: sk-xxx"
UPDATE secret_scan_rules
SET pattern = '(?i)(api[_-]?key|apikey)\\s*[=:]\\s*[\'\"]?[A-Za-z0-9._-]{8,}[\'\"]?',
    updated_at = NOW()
WHERE code = 'SECRET-010' AND is_builtin = true;
