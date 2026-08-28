package config

import (
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Debug    bool   `yaml:"debug"`
	Port     int    `yaml:"port"`
	DSN      string `yaml:"dsn"`
	Database string `yaml:"database"`

	// Database connection details (alternative to DSN)
	DBHost     string `yaml:"db_host"`
	DBPort     int    `yaml:"db_port"`
	DBUser     string `yaml:"db_user"`
	DBPassword string `yaml:"db_password"`
	DBName     string `yaml:"db_name"`

	EncryptKey           string `yaml:"encrypt_key"`
	SyncInterval         int    `yaml:"sync_interval"`
	GitlabToken          string `yaml:"gitlab_token"`
	TaskTimeoutMin       int    `yaml:"task_timeout_min"`
	MaxParallelTask      int    `yaml:"max_parallel_task"`
	ProjectBaseDir       string `yaml:"project_base_dir"`
	FrontendPath         string `yaml:"frontend_path"`
	GraphCacheTTLMinutes int    `yaml:"graph_cache_ttl_minutes"`

	// GitLab OAuth 配置
	GitlabOAuthEnabled        bool   `yaml:"gitlab_oauth_enabled"`
	GitlabBaseURL             string `yaml:"gitlab_base_url"`
	GitlabOAuthClientID       string `yaml:"gitlab_oauth_client_id"`
	GitlabOAuthClientSecret   string `yaml:"gitlab_oauth_client_secret"`
	GitlabOAuthRedirectURI    string `yaml:"gitlab_oauth_redirect_uri"`
	GitlabOAuthAutoCreateUser bool   `yaml:"gitlab_oauth_auto_create_user"`

	// Diff Line Map 配置
	DiffLineMap DiffLineMapConfig `yaml:"diff_line_map"`
}

// DiffLineMapConfig diff 行号映射系统配置
type DiffLineMapConfig struct {
	Enabled           bool   `yaml:"enabled"`             // 是否启用系统
	InjectLineNumbers bool   `yaml:"inject_line_numbers"` // 是否注入 [new|old] 行号前缀
	UseCorrelator     bool   `yaml:"use_correlator"`      // 是否启用后端校正
	StoreDiffRefs     bool   `yaml:"store_diff_refs"`     // 是否存储 diff_refs
	GrayPercent       int    `yaml:"gray_percent"`        // 灰度百分比 0-100（0=关闭灰度，仅总开关控制；>0 时只有命中灰度的任务才启用）
	GrayMode          string `yaml:"gray_mode"`           // 灰度模式："random" 或 "project_hash"
}

// Validate 检查配置一致性，返回发现的警告信息
// 规则：子功能开启但总开关关闭时，视为配置错误
func (cfg DiffLineMapConfig) Validate() []string {
	var warnings []string
	if !cfg.Enabled {
		if cfg.InjectLineNumbers {
			warnings = append(warnings, "inject_line_numbers=true but enabled=false, injection will not work")
		}
		if cfg.UseCorrelator {
			warnings = append(warnings, "use_correlator=true but enabled=false, correlator will not work")
		}
		if cfg.StoreDiffRefs {
			warnings = append(warnings, "store_diff_refs=true but enabled=false, diff refs will not be stored")
		}
	}
	if cfg.GrayPercent < 0 || cfg.GrayPercent > 100 {
		warnings = append(warnings, "gray_percent should be 0-100")
	}
	if cfg.GrayPercent > 0 && cfg.Enabled {
		if cfg.GrayMode != "random" && cfg.GrayMode != "project_hash" {
			warnings = append(warnings, "gray_mode should be 'random' or 'project_hash'")
		}
	}
	return warnings
}

// IsGrayEnabled 判断指定项目是否命中灰度
// 当 GrayPercent <= 0 时，直接返回 true（不做灰度分流，由总开关控制）
// 当 GrayPercent > 0 时，按 GrayMode 计算是否命中
func (cfg DiffLineMapConfig) IsGrayEnabled(projectID uint) bool {
	if cfg.GrayPercent <= 0 || cfg.GrayPercent >= 100 {
		return true
	}
	var hash uint64
	switch cfg.GrayMode {
	case "project_hash":
		// 【R8 修复】使用标准 hash/fnv64 替代自定义常数，碰撞特性更可预期
		hash = hashProjectID(projectID)
	default:
		// 【R9 修复】random 模式改为基于“项目ID + 当前日期”的日级别伪随机，
		// 保证同一项目同一天内始终命中或始终不命中，避免同一 MR 并发任务行为不一致。
		// 若需要完全随机（如每请求级别），可另行配置 gray_mode="full_random"。
		salt := time.Now().Format("20060102")
		hash = hashString(fmt.Sprintf("%d-%s", projectID, salt))
	}
	return int(hash%100) < cfg.GrayPercent
}

// hashProjectID 使用 FNV-1a 64-bit 对项目 ID 做确定性哈希
func hashProjectID(projectID uint) uint64 {
	h := fnv.New64a()
	// 将 uint 转为二进制写入，避免字符串解析歧义
	buf := make([]byte, 8)
	// 按小端序写入
	for i := range buf {
		buf[i] = byte(projectID >> (i * 8))
	}
	_, _ = h.Write(buf)
	return h.Sum64()
}

// hashString 使用 FNV-1a 64-bit 对字符串做哈希
func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func Load() *Config {
	cfg := &Config{
		Debug:    getEnvBool("DEBUG", false),
		Port:     getEnvInt("PORT", 8080),
		DSN:      getEnv("DSN", ""),
		Database: getEnv("DATABASE", "mysql"),

		DBHost:     getEnv("DB_HOST", "127.0.0.1"),
		DBPort:     getEnvInt("DB_PORT", 3306),
		DBUser:     getEnv("DB_USER", "root"),
		DBPassword: getEnv("DB_PASSWORD", ""),
		DBName:     getEnv("DB_NAME", "ai_optimizer"),

		EncryptKey:           getEnv("ENCRYPTION_KEY", ""),
		SyncInterval:         getEnvInt("SYNC_INTERVAL", 60),
		GitlabToken:          getEnv("GITLAB_TOKEN", ""),
		TaskTimeoutMin:       getEnvInt("TASK_TIMEOUT_MIN", 30),
		MaxParallelTask:      getEnvInt("MAX_PARALLEL_TASK", 20),
		ProjectBaseDir:       getEnv("PROJECT_BASE_DIR", "/data/gitlab/"),
		FrontendPath:         getEnv("FRONTEND_PATH", "/app/prototype"),
		GraphCacheTTLMinutes: getEnvInt("CODEGUARD_GRAPH_CACHE_TTL_MINUTES", 30),

		// GitLab OAuth
		GitlabOAuthEnabled:        getEnvBool("GITLAB_OAUTH_ENABLED", false),
		GitlabBaseURL:             getEnv("GITLAB_BASE_URL", ""),
		GitlabOAuthClientID:       getEnv("GITLAB_OAUTH_CLIENT_ID", ""),
		GitlabOAuthClientSecret:   getEnv("GITLAB_OAUTH_CLIENT_SECRET", ""),
		GitlabOAuthRedirectURI:    getEnv("GITLAB_OAUTH_REDIRECT_URI", ""),
		GitlabOAuthAutoCreateUser: getEnvBool("GITLAB_OAUTH_AUTO_CREATE_USER", true),

		// Diff Line Map（默认关闭，需显式开启）
		DiffLineMap: DiffLineMapConfig{
			Enabled:           getEnvBool("DIFF_LINE_MAP_ENABLED", false),
			InjectLineNumbers: getEnvBool("DIFF_LINE_MAP_INJECT", false),
			UseCorrelator:     getEnvBool("DIFF_LINE_MAP_CORRELATOR", false),
			StoreDiffRefs:     getEnvBool("DIFF_LINE_MAP_STORE_DIFF_REFS", false),
			GrayPercent:       getEnvInt("DIFF_LINE_MAP_GRAY_PERCENT", 0),
			GrayMode:          getEnv("DIFF_LINE_MAP_GRAY_MODE", "project_hash"),
		},
	}
	return cfg
}

func (c *Config) GetDSN() string {
	if c.DSN != "" {
		return c.DSN
	}
	return c.buildDSN()
}

func (c *Config) buildDSN() string {
	switch c.Database {
	case "postgres":
		return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=disable TimeZone=Asia/Shanghai",
			c.DBHost, c.DBUser, c.DBPassword, c.DBName, c.DBPort)
	default:
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName)
	}
}

func (c *Config) GetGormDialector() string {
	return c.Database
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		b, _ := strconv.ParseBool(v)
		return b
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		i, _ := strconv.Atoi(v)
		return i
	}
	return defaultVal
}
