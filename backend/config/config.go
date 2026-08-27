package config

import (
	"fmt"
	"os"
	"strconv"
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
	Enabled           bool `yaml:"enabled"`             // 是否启用系统
	InjectLineNumbers bool `yaml:"inject_line_numbers"` // 是否注入 [new|old] 行号前缀
	UseCorrelator     bool `yaml:"use_correlator"`      // 是否启用后端校正
	StoreDiffRefs     bool `yaml:"store_diff_refs"`     // 是否存储 diff_refs
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
	return warnings
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
