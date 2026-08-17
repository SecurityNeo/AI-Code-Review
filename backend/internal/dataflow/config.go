package dataflow

// --- Taint Source Patterns ---

// MatchType defines how a pattern is matched against AST call sites.
type MatchType string

const (
	// MatchMethodCall matches method invocations like c.Query() or req.Get().
	MatchMethodCall MatchType = "method_call"
	// MatchFieldAccess matches field/property access like request.args.
	MatchFieldAccess MatchType = "field_access"
	// MatchFunctionCall matches standalone function calls like os.Args().
	MatchFunctionCall MatchType = "function_call"
	// MatchAnnotation matches parameter annotations like @RequestParam.
	MatchAnnotation MatchType = "annotation"
)

// SourcePattern defines a taint source (e.g., user input from HTTP parameters).
type SourcePattern struct {
	Kind      string    // e.g., http_param, header, body, cookie, file, env.
	Type      MatchType // How the pattern is matched.
	Pattern   string    // The match pattern string.
	Framework string    // e.g., gin, echo, flask, fastapi, spring, express.
}

// SinkPattern defines a dangerous sink (e.g., SQL execution, command execution).
type SinkPattern struct {
	Kind      string // e.g., sql_injection, command_injection, xss.
	Type      MatchType
	Pattern   string
	Severity  string // high, medium, or low.
	CWE       string
	Framework string // Optional framework-specific sink.
}

// SanitizerPattern defines a function that neutralizes taint (e.g., html.EscapeString).
type SanitizerPattern struct {
	Kind    string // The sink kind it protects against, or "all".
	Pattern string
}

// LanguageConfig holds taint rules for a single language.
type LanguageConfig struct {
	Language   string // e.g., golang, java, python, javascript.
	Sources    []SourcePattern
	Sinks      []SinkPattern
	Sanitizers []SanitizerPattern
}

// TaintConfig holds per-language taint rule sets.
type TaintConfig struct {
	Configs map[string]*LanguageConfig // key: normalized language name.
}

// NewDefaultTaintConfig returns built-in rules for Go, Java, Python and JavaScript.
func NewDefaultTaintConfig() *TaintConfig {
	return &TaintConfig{
		Configs: map[string]*LanguageConfig{
			"golang":     golangConfig(),
			"java":       javaConfig(),
			"python":     pythonConfig(),
			"javascript": javascriptConfig(),
		},
	}
}

// --- Go configuration ---
func golangConfig() *LanguageConfig {
	return &LanguageConfig{
		Language: "golang",
		Sources: []SourcePattern{ // Gin
			{Kind: SourceHTTPParam, Type: MatchMethodCall, Pattern: "Query", Framework: "gin"},
			{Kind: SourceHTTPParam, Type: MatchMethodCall, Pattern: "Param", Framework: "gin"},
			{Kind: SourceQueryParam, Type: MatchMethodCall, Pattern: "QueryArray", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "PostForm", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "PostFormArray", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "FormFile", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "MultipartForm", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "ShouldBind", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "ShouldBindJSON", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "ShouldBindXML", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "ShouldBindQuery", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "BindJSON", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "BindXML", Framework: "gin"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "BindQuery", Framework: "gin"},
			{Kind: SourceHeader, Type: MatchMethodCall, Pattern: "GetHeader", Framework: "gin"},
			{Kind: SourceHeader, Type: MatchMethodCall, Pattern: "Header", Framework: "gin"},
			{Kind: SourceCookie, Type: MatchMethodCall, Pattern: "Cookie", Framework: "gin"},
			// Echo
			{Kind: SourceHTTPParam, Type: MatchMethodCall, Pattern: "QueryParam", Framework: "echo"},
			{Kind: SourceHTTPParam, Type: MatchMethodCall, Pattern: "PathParam", Framework: "echo"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "FormValue", Framework: "echo"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "FormFile", Framework: "echo"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "Bind", Framework: "echo"},
			{Kind: SourceHeader, Type: MatchMethodCall, Pattern: "RequestHeader", Framework: "echo"},
			{Kind: SourceCookie, Type: MatchMethodCall, Pattern: "Cookie", Framework: "echo"},
			// Standard library
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "URL.Query", Framework: "standard"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "Body", Framework: "standard"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "Header.Get", Framework: "standard"},
			{Kind: SourceCookie, Type: MatchFieldAccess, Pattern: "Cookie", Framework: "standard"},
			{Kind: SourceEnv, Type: MatchFunctionCall, Pattern: "os.Getenv", Framework: "standard"},
			{Kind: SourceFile, Type: MatchFunctionCall, Pattern: "os.Args", Framework: "standard"},
		},
		Sinks: []SinkPattern{
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "Exec", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "ExecContext", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "Query", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "QueryContext", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "QueryRow", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "QueryRowContext", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "Raw", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "Prepare", Severity: "medium", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "PrepareContext", Severity: "medium", CWE: "CWE-89"},
			{Kind: SinkCommandInjection, Type: MatchFunctionCall, Pattern: "exec.Command", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchFunctionCall, Pattern: "exec.CommandContext", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "Start", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "Run", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchFunctionCall, Pattern: "syscall.Start", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "WriteString", Severity: "medium", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchFunctionCall, Pattern: "Sprintf", Severity: "low", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchFunctionCall, Pattern: "Fprintf", Severity: "low", CWE: "CWE-79"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "os.OpenFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "os.ReadFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "os.WriteFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "os.Create", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "os.Mkdir", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "os.MkdirAll", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "ioutil.ReadFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFunctionCall, Pattern: "ioutil.WriteFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "http.Get", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "http.Post", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "http.Do", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "net.Dial", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "net.DialContext", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "json.Unmarshal", Severity: "low", CWE: "CWE-502"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "gob.Decode", Severity: "low", CWE: "CWE-502"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "proto.Unmarshal", Severity: "low", CWE: "CWE-502"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "yaml.Unmarshal", Severity: "low", CWE: "CWE-502"},
		},
		Sanitizers: []SanitizerPattern{
			{Kind: SinkSQLInjection, Pattern: "Prepare"},
			{Kind: SinkSQLInjection, Pattern: "sql.Quote"},
			{Kind: SinkXSS, Pattern: "html.EscapeString"},
			{Kind: SinkXSS, Pattern: "template.HTMLEscapeString"},
			{Kind: "all", Pattern: "regexp.Match"},
			{Kind: "all", Pattern: "strings.ReplaceAll"},
			{Kind: "all", Pattern: "strings.TrimSpace"},
			{Kind: "all", Pattern: "strconv.Atoi"},
			{Kind: "all", Pattern: "strconv.ParseInt"},
			{Kind: "all", Pattern: "strconv.ParseFloat"},
			{Kind: "all", Pattern: "strconv.ParseBool"},
			{Kind: "all", Pattern: "uuid.Parse"},
			{Kind: "all", Pattern: "time.Parse"},
		},
	}
}

// --- Java 配置 ---
func javaConfig() *LanguageConfig {
	return &LanguageConfig{
		Language: "java",
		Sources: []SourcePattern{
			// Spring
			{Kind: SourceHTTPParam, Type: MatchAnnotation, Pattern: "@RequestParam", Framework: "spring"},
			{Kind: SourceHTTPParam, Type: MatchAnnotation, Pattern: "@PathVariable", Framework: "spring"},
			{Kind: SourceBody, Type: MatchAnnotation, Pattern: "@RequestBody", Framework: "spring"},
			{Kind: SourceHeader, Type: MatchAnnotation, Pattern: "@RequestHeader", Framework: "spring"},
			{Kind: SourceCookie, Type: MatchAnnotation, Pattern: "@CookieValue", Framework: "spring"},
			// Servlet / Jakarta
			{Kind: SourceHTTPParam, Type: MatchMethodCall, Pattern: "getParameter", Framework: "servlet"},
			{Kind: SourceHTTPParam, Type: MatchMethodCall, Pattern: "getPathInfo", Framework: "servlet"},
			{Kind: SourceBody, Type: MatchMethodCall, Pattern: "getInputStream", Framework: "servlet"},
			{Kind: SourceHeader, Type: MatchMethodCall, Pattern: "getHeader", Framework: "servlet"},
			{Kind: SourceCookie, Type: MatchMethodCall, Pattern: "getCookie", Framework: "servlet"},
		},
		Sinks: []SinkPattern{
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "createStatement", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "executeQuery", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "executeUpdate", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "execute", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "prepareStatement", Severity: "medium", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "query", Severity: "high", CWE: "CWE-89", Framework: "jdbctemplate"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "Runtime.exec", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "ProcessBuilder.start", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "print", Severity: "medium", CWE: "CWE-79", Framework: "servlet"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "println", Severity: "medium", CWE: "CWE-79", Framework: "servlet"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "write", Severity: "medium", CWE: "CWE-79", Framework: "servlet"},
			{Kind: SinkPathTraversal, Type: MatchMethodCall, Pattern: "FileInputStream", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchMethodCall, Pattern: "FileOutputStream", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchMethodCall, Pattern: "FileReader", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchMethodCall, Pattern: "FileWriter", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkSSRF, Type: MatchMethodCall, Pattern: "URL.openConnection", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchMethodCall, Pattern: "HttpClient.send", Severity: "medium", CWE: "CWE-918"},
		},
		Sanitizers: []SanitizerPattern{
			{Kind: SinkSQLInjection, Pattern: "PreparedStatement"},
			{Kind: SinkSQLInjection, Pattern: "HibernateQuery.setParameter"},
			{Kind: SinkXSS, Pattern: "HtmlUtils.htmlEscape"},
			{Kind: SinkXSS, Pattern: "StringEscapeUtils.escapeHtml4"},
			{Kind: SinkXSS, Pattern: "Jsoup.clean"},
			{Kind: "all", Pattern: "Pattern.matches"},
			{Kind: "all", Pattern: "String.replace"},
			{Kind: "all", Pattern: "Integer.parseInt"},
			{Kind: "all", Pattern: "Long.parseLong"},
			{Kind: "all", Pattern: "UUID.fromString"},
		},
	}
}

// --- Python 配置 ---
func pythonConfig() *LanguageConfig {
	return &LanguageConfig{
		Language: "python",
		Sources: []SourcePattern{
			// Flask
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.args", Framework: "flask"},
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.args.get", Framework: "flask"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.form", Framework: "flask"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.json", Framework: "flask"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.data", Framework: "flask"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.files", Framework: "flask"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "request.headers", Framework: "flask"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "request.headers.get", Framework: "flask"},
			{Kind: SourceCookie, Type: MatchFieldAccess, Pattern: "request.cookies", Framework: "flask"},
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.view_args", Framework: "flask"},
			// FastAPI
			{Kind: SourceBody, Type: MatchAnnotation, Pattern: "Body", Framework: "fastapi"},
			{Kind: SourceHTTPParam, Type: MatchAnnotation, Pattern: "Query", Framework: "fastapi"},
			{Kind: SourceHTTPParam, Type: MatchAnnotation, Pattern: "Path", Framework: "fastapi"},
			{Kind: SourceHeader, Type: MatchAnnotation, Pattern: "Header", Framework: "fastapi"},
			{Kind: SourceCookie, Type: MatchAnnotation, Pattern: "Cookie", Framework: "fastapi"},
			{Kind: SourceBody, Type: MatchAnnotation, Pattern: "Form", Framework: "fastapi"},
			// Django
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.GET", Framework: "django"},
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.GET.get", Framework: "django"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.POST", Framework: "django"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.POST.get", Framework: "django"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.body", Framework: "django"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "request.headers", Framework: "django"},
			{Kind: SourceCookie, Type: MatchFieldAccess, Pattern: "request.COOKIES", Framework: "django"},
			// Standard
			{Kind: SourceFile, Type: MatchFieldAccess, Pattern: "sys.argv", Framework: "standard"},
			{Kind: SourceEnv, Type: MatchFunctionCall, Pattern: "os.environ.get", Framework: "standard"},
			{Kind: SourceEnv, Type: MatchFunctionCall, Pattern: "os.getenv", Framework: "standard"},
		},
		Sinks: []SinkPattern{
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "execute", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "executemany", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "cursor.execute", Severity: "high", CWE: "CWE-89"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "raw", Severity: "high", CWE: "CWE-89", Framework: "django"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "os.system", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "os.popen", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "subprocess.call", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "subprocess.run", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "subprocess.Popen", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "eval", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchMethodCall, Pattern: "exec", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "render_template_string", Severity: "medium", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "mark_safe", Severity: "medium", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchMethodCall, Pattern: "write", Severity: "low", CWE: "CWE-79"},
			{Kind: SinkPathTraversal, Type: MatchMethodCall, Pattern: "open", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchMethodCall, Pattern: "os.path.join", Severity: "low", CWE: "CWE-22"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "requests.get", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "requests.post", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFunctionCall, Pattern: "urllib.request.urlopen", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "pickle.loads", Severity: "high", CWE: "CWE-502"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "yaml.load", Severity: "medium", CWE: "CWE-502"},
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "json.loads", Severity: "low", CWE: "CWE-502"},
		},
		Sanitizers: []SanitizerPattern{
			{Kind: SinkSQLInjection, Pattern: "cursor.execute"}, // 带参数化
			{Kind: SinkSQLInjection, Pattern: "ORM.query.filter"},
			{Kind: SinkXSS, Pattern: "bleach.clean"},
			{Kind: SinkXSS, Pattern: "html.escape"},
			{Kind: "all", Pattern: "re.match"},
			{Kind: "all", Pattern: "re.search"},
			{Kind: "all", Pattern: "str.replace"},
			{Kind: "all", Pattern: "int("},
			{Kind: "all", Pattern: "float("},
			{Kind: "all", Pattern: "uuid.UUID"},
		},
	}
}

// --- JavaScript/TypeScript 配置 ---
func javascriptConfig() *LanguageConfig {
	return &LanguageConfig{
		Language: "javascript",
		Sources: []SourcePattern{
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "req.params", Framework: "express"},
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "req.query", Framework: "express"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "req.body", Framework: "express"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "req.headers", Framework: "express"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "req.get", Framework: "express"},
			{Kind: SourceCookie, Type: MatchFieldAccess, Pattern: "req.cookies", Framework: "express"},
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.params", Framework: "fastify"},
			{Kind: SourceHTTPParam, Type: MatchFieldAccess, Pattern: "request.query", Framework: "fastify"},
			{Kind: SourceBody, Type: MatchFieldAccess, Pattern: "request.body", Framework: "fastify"},
			{Kind: SourceHeader, Type: MatchFieldAccess, Pattern: "request.headers", Framework: "fastify"},
			{Kind: SourceCookie, Type: MatchFieldAccess, Pattern: "request.cookies", Framework: "fastify"},
			{Kind: SourceEnv, Type: MatchFieldAccess, Pattern: "process.env", Framework: "standard"},
			{Kind: SourceFile, Type: MatchFieldAccess, Pattern: "process.argv", Framework: "standard"},
		},
		Sinks: []SinkPattern{
			// Command Injection (放在 exec 之前，避免被 SQL injection 抢先)
			{Kind: SinkCommandInjection, Type: MatchFunctionCall, Pattern: "child_process.exec", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchFunctionCall, Pattern: "child_process.execSync", Severity: "high", CWE: "CWE-78"},
			{Kind: SinkCommandInjection, Type: MatchFunctionCall, Pattern: "child_process.spawn", Severity: "high", CWE: "CWE-78"},
			// SQL Injection
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "query", Severity: "high", CWE: "CWE-89", Framework: "mysql"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "query", Severity: "high", CWE: "CWE-89", Framework: "pg"},
			{Kind: SinkSQLInjection, Type: MatchMethodCall, Pattern: "exec", Severity: "high", CWE: "CWE-89", Framework: "sqlite3"},
			// XSS
			{Kind: SinkXSS, Type: MatchFieldAccess, Pattern: "innerHTML", Severity: "high", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchFieldAccess, Pattern: "outerHTML", Severity: "high", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchFieldAccess, Pattern: "document.write", Severity: "medium", CWE: "CWE-79"},
			{Kind: SinkXSS, Type: MatchFieldAccess, Pattern: "document.writeln", Severity: "medium", CWE: "CWE-79"},
			// Path Traversal
			{Kind: SinkPathTraversal, Type: MatchFieldAccess, Pattern: "fs.readFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFieldAccess, Pattern: "fs.readFileSync", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFieldAccess, Pattern: "fs.writeFile", Severity: "medium", CWE: "CWE-22"},
			{Kind: SinkPathTraversal, Type: MatchFieldAccess, Pattern: "fs.writeFileSync", Severity: "medium", CWE: "CWE-22"},
			// SSRF
			{Kind: SinkSSRF, Type: MatchFieldAccess, Pattern: "fetch", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFieldAccess, Pattern: "axios.get", Severity: "medium", CWE: "CWE-918"},
			{Kind: SinkSSRF, Type: MatchFieldAccess, Pattern: "axios.post", Severity: "medium", CWE: "CWE-918"},
			// Deserialization
			{Kind: SinkDeserialization, Type: MatchFunctionCall, Pattern: "JSON.parse", Severity: "low", CWE: "CWE-502"},
			{Kind: SinkDeserialization, Type: MatchMethodCall, Pattern: "eval", Severity: "high", CWE: "CWE-502"},
		},
		Sanitizers: []SanitizerPattern{
			{Kind: SinkSQLInjection, Pattern: "mysql.escape"},
			{Kind: SinkSQLInjection, Pattern: "pg.escapeIdentifier"},
			{Kind: SinkXSS, Pattern: "DOMPurify.sanitize"},
			{Kind: SinkXSS, Pattern: "escapeHtml"},
			{Kind: "all", Pattern: "validator.isInt"},
			{Kind: "all", Pattern: "validator.isURL"},
			{Kind: "all", Pattern: "validator.isEmail"},
			{Kind: "all", Pattern: "parseInt"},
			{Kind: "all", Pattern: "parseFloat"},
		},
	}
}

// GetLanguageConfig 获取指定语言的配置
func (tc *TaintConfig) GetLanguageConfig(lang string) *LanguageConfig {
	if c, ok := tc.Configs[lang]; ok {
		return c
	}
	// Fallback: try normalized name
	if c, ok := tc.Configs[normalizeLang(lang)]; ok {
		return c
	}
	return golangConfig() // 默认回退到 Go
}

func normalizeLang(lang string) string {
	switch lang {
	case "go", "golang", "Go":
		return "golang"
	case "java", "Java":
		return "java"
	case "python", "Python":
		return "python"
	case "javascript", "js", "JavaScript", "typescript", "ts":
		return "javascript"
	default:
		return lang
	}
}
