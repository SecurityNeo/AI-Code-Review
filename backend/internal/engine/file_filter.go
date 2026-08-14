package engine

import (
	"encoding/json"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/gitlab"
)

// FileFilterConfig 文件过滤配置（存储在 ReviewAgentConfig.StageConfigs["file_filter"]）
type FileFilterConfig struct {
	// 启用默认过滤规则（按项目语言自动选择）
	EnableDefaultFilters bool `json:"enable_default_filters"`

	// 自定义排除规则（glob 语法）
	ExcludePatterns []string `json:"exclude_patterns,omitempty"`

	// 自定义包含规则（覆盖排除规则，glob 语法）
	IncludePatterns []string `json:"include_patterns,omitempty"`
}

// DefaultFileFilterConfig 返回默认配置
func DefaultFileFilterConfig() FileFilterConfig {
	return FileFilterConfig{
		EnableDefaultFilters: true,
		ExcludePatterns:      []string{},
		IncludePatterns:      []string{},
	}
}

// LoadFileFilterConfig 从 ReviewAgentConfig 读取文件过滤配置
func LoadFileFilterConfig(agentCfg model.ReviewAgentConfig) FileFilterConfig {
	cfg := DefaultFileFilterConfig()

	sc := agentCfg.StageConfig("file_filter")
	if sc == nil {
		return cfg
	}

	if v, ok := sc["enable_default_filters"].(bool); ok {
		cfg.EnableDefaultFilters = v
	}
	if v, ok := sc["exclude_patterns"].([]interface{}); ok {
		for _, item := range v {
			if s, ok := item.(string); ok {
				cfg.ExcludePatterns = append(cfg.ExcludePatterns, s)
			}
		}
	}
	if v, ok := sc["include_patterns"].([]interface{}); ok {
		for _, item := range v {
			if s, ok := item.(string); ok {
				cfg.IncludePatterns = append(cfg.IncludePatterns, s)
			}
		}
	}

	return cfg
}

// FilterStats 过滤统计
type FilterStats struct {
	Total         int      `json:"total"`
	Kept          int      `json:"kept"`
	Filtered      int      `json:"filtered"`
	FilteredPaths []string `json:"filtered_paths,omitempty"`
}

// MarshalFilterStats 将过滤统计序列化为 JSON 字符串
func MarshalFilterStats(stats FilterStats) string {
	b, err := json.Marshal(stats)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// FilterDiffFiles 对 diff 文件列表进行过滤
// 返回过滤后的文件列表和统计信息
func FilterDiffFiles(files []gitlab.DiffFile, matcher *FileMatcher) ([]gitlab.DiffFile, FilterStats) {
	var filtered []gitlab.DiffFile
	stats := FilterStats{}

	for _, f := range files {
		stats.Total++

		path := f.NewPath
		if path == "" {
			path = f.OldPath
		}
		if path == "" {
			filtered = append(filtered, f)
			stats.Kept++
			continue
		}

		if matcher.ShouldSkip(path) {
			stats.Filtered++
			stats.FilteredPaths = append(stats.FilteredPaths, path)
		} else {
			filtered = append(filtered, f)
			stats.Kept++
		}
	}

	return filtered, stats
}
