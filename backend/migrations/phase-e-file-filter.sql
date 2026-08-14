-- 新增 diff 文件过滤统计字段
ALTER TABLE tasks
ADD COLUMN diff_filter_stats JSON DEFAULT '{}' COMMENT 'diff 文件过滤统计信息，如 {"total": 50, "kept": 35, "filtered": 15, "filtered_paths": [...]}'
AFTER diff_files_json;
