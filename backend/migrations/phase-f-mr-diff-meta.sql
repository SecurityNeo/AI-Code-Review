-- Migration: create mr_diff_meta table for diff line map system
-- This table stores lightweight index of MR diffs; raw diff text is kept in object storage.

CREATE TABLE IF NOT EXISTS `mr_diff_meta` (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `created_at` DATETIME(3) NULL,
    `updated_at` DATETIME(3) NULL,
    `deleted_at` DATETIME(3) NULL,
    `task_id` BIGINT UNSIGNED NOT NULL,
    `project_id` BIGINT UNSIGNED NOT NULL,
    `mr_iid` INT NOT NULL,
    `object_key` VARCHAR(512) NOT NULL DEFAULT '',
    `base_sha` VARCHAR(64) NOT NULL DEFAULT '',
    `head_sha` VARCHAR(64) NOT NULL DEFAULT '',
    `start_sha` VARCHAR(64) NOT NULL DEFAULT '',
    `line_map_json` LONGTEXT,
    INDEX `idx_mr_diff_meta_task_id` (`task_id`),
    INDEX `idx_mr_diff_meta_project_id` (`project_id`),
    INDEX `idx_mr_diff_meta_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
