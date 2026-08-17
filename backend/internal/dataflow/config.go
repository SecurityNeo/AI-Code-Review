package dataflow

// SinkConfig Sink配置（可扩展）
type SinkConfig struct {
	Kind     string   `json:"kind"`
	Patterns []string `json:"patterns"`
	Severity string   `json:"severity"` // high | medium | low
	CWE      string   `json:"cwe,omitempty"`
}

// DefaultSinkConfigs 默认Sink配置
var DefaultSinkConfigs = []SinkConfig{
	{
		Kind:     SinkSQLInjection,
		Patterns: []string{"Query", "Exec", "Raw", "db.Query", "db.Exec", "sqlx", "gorm.DB.Raw"},
		Severity: "high",
		CWE:      "CWE-89",
	},
	{
		Kind:     SinkCommandInjection,
		Patterns: []string{"exec.Command", "exec.CommandContext", "syscall.Start", "os.StartProcess"},
		Severity: "high",
		CWE:      "CWE-78",
	},
	{
		Kind:     SinkXSS,
		Patterns: []string{"w.Write", "Fprintf", "Sprintf", `fmt\.Printf`, `\.innerHTML`, `\.outerHTML`},
		Severity: "medium",
		CWE:      "CWE-79",
	},
	{
		Kind:     SinkPathTraversal,
		Patterns: []string{"os.OpenFile", "os.WriteFile", "os.Create", "os.Mkdir", "os.ReadFile", "ioutil.ReadFile", "ioutil.WriteFile"},
		Severity: "medium",
		CWE:      "CWE-22",
	},
	{
		Kind:     SinkSSRF,
		Patterns: []string{"http.Get", "http.Post", "http.Do", "net.Dial", "net.DialContext"},
		Severity: "medium",
		CWE:      "CWE-918",
	},
	{
		Kind:     SinkDeserialization,
		Patterns: []string{"json.Unmarshal", "gob.Decode", "proto.Unmarshal", "yaml.Unmarshal"},
		Severity: "low",
		CWE:      "CWE-502",
	},
}

// SourceConfig Source配置
type SourceConfig struct {
	Kind     string   `json:"kind"`
	Patterns []string `json:"patterns"`
}

// DefaultSourceConfigs 默认Source配置
var DefaultSourceConfigs = []SourceConfig{
	{
		Kind:     SourceHTTPParam,
		Patterns: []string{"Query", "Param", "PostForm", "GetHeader", "Cookie", "FormValue", "GetQuery"},
	},
	{
		Kind:     SourceHeader,
		Patterns: []string{"GetHeader", `Header\.Get`, `r\.Header`},
	},
	{
		Kind:     SourceBody,
		Patterns: []string{"BindJSON", "Bind", `ctx\.Body`, `req\.Body`},
	},
	{
		Kind:     SourceDB,
		Patterns: []string{"QueryRow", "Select", "Get", "Find"},
	},
}
