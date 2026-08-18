package parser

// SourceLocation 源码位置
type SourceLocation struct {
	File        string `json:"file"`
	LineStart   int    `json:"line_start"`
	LineEnd     int    `json:"line_end"`
	ColumnStart int    `json:"column_start"`
	ColumnEnd   int    `json:"column_end"`
}

// UnifiedAST 单次解析结果（所有语言共用）
type UnifiedAST struct {
	Language    string              `json:"language"`
	FilePath    string              `json:"file_path"`
	Functions   []UnifiedFunction   `json:"functions"`
	Types       []UnifiedType       `json:"types"`
	Imports     []UnifiedImport     `json:"imports"`
	Variables   []UnifiedVariable   `json:"variables,omitempty"`
	Constants   []UnifiedConstant   `json:"constants,omitempty"`
	Endpoints   []UnifiedEndpoint   `json:"endpoints,omitempty"`    // 框架适配器产出
	CallSites   []UnifiedCallSite   `json:"call_sites,omitempty"`   // 本文件内的调用点
	VarBindings []UnifiedVarBinding `json:"var_bindings,omitempty"` // 变量赋值绑定
	TODOComments []TODOComment      `json:"todo_comments,omitempty"` // P2-3 TODO/FIXME/HACK扫描
}

// TODOComment TODO/FIXME/HACK 注释
type TODOComment struct {
	Text     string `json:"text"`
	Type     string `json:"type"`     // todo | fixme | hack | xxx
	Line     int    `json:"line"`
	Function string `json:"function,omitempty"` // 所在函数名（如果能推断）
}

// UnifiedFunction 函数定义
type UnifiedFunction struct {
	Name         string         `json:"name"`
	Receiver     string         `json:"receiver,omitempty"`
	Params       []UnifiedParam `json:"params"`
	Returns      []string       `json:"returns"`
	IsExported   bool           `json:"is_exported"`
	IsAsync      bool           `json:"is_async"`
	IsStatic     bool           `json:"is_static"`
	IsAbstract   bool           `json:"is_abstract"`
	IsVariadic   bool           `json:"is_variadic"`
	Generics     []string       `json:"generics,omitempty"`
	Annotations  []string       `json:"annotations,omitempty"`
	DocComment   string         `json:"doc_comment,omitempty"`
	BodySnippet  string         `json:"body_snippet,omitempty"`
	SecurityRole string         `json:"security_role,omitempty"` // P2-1 安全角色：auth | encryption | sanitizer | validator | handler | service | data_access | other
	Complexity   int            `json:"complexity,omitempty"`    // P2-5 简单圈复杂度估算
	LOC          int            `json:"loc,omitempty"`           // P2-5 函数体行数
	NestedDepth  int            `json:"nested_depth,omitempty"`  // P2-5 最大嵌套深度
	Location     SourceLocation `json:"location"`
}

// UnifiedParam 参数
type UnifiedParam struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Optional    bool     `json:"optional"`
	IsVariadic  bool     `json:"is_variadic"`
	Annotations []string `json:"annotations,omitempty"` // 参数级注解（Java @RequestParam / Python FastAPI Body/Query 等）
}

// UnifiedType 类型定义
type UnifiedType struct {
	Name       string                   `json:"name"`
	Kind       string                   `json:"kind"` // struct | interface | class | enum | type_alias
	Generics   []string                 `json:"generics,omitempty"`
	Fields     []UnifiedField           `json:"fields,omitempty"`
	Methods    []UnifiedMethodSignature `json:"methods,omitempty"`
	Implements []string                 `json:"implements,omitempty"`
	Extends    []string                 `json:"extends,omitempty"`
	IsExported bool                     `json:"is_exported"`
	DocComment string                   `json:"doc_comment,omitempty"`
	Location   SourceLocation           `json:"location"`
}

// UnifiedMethodSignature 方法签名
type UnifiedMethodSignature struct {
	Name       string         `json:"name"`
	Params     []UnifiedParam `json:"params"`
	Returns    []string       `json:"returns"`
	IsExported bool           `json:"is_exported"`
	Location   SourceLocation `json:"location"`
}

// UnifiedField 字段定义
type UnifiedField struct {
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Tag          string         `json:"tag,omitempty"` // Go json tag, Java annotation
	DefaultValue string         `json:"default_value,omitempty"`
	IsExported   bool           `json:"is_exported"`
	IsStatic     bool           `json:"is_static"`
	IsFinal      bool           `json:"is_final"`
	Location     SourceLocation `json:"location,omitempty"`
}

// UnifiedImport 导入
type UnifiedImport struct {
	Path     string `json:"path"`
	Alias    string `json:"alias,omitempty"`
	IsStdLib bool   `json:"is_stdlib"`
}

// UnifiedCallSite 调用点
type UnifiedCallSite struct {
	CallerFunc  string         `json:"caller_func"`
	TargetPkg   string         `json:"target_pkg,omitempty"`
	TargetFunc  string         `json:"target_func"`
	Arguments   []string       `json:"arguments,omitempty"`
	ReceiverVar string         `json:"receiver_var,omitempty"` // selector_expression 的 receiver 对象变量名
	Location    SourceLocation `json:"location"`
}

// UnifiedVarBinding 变量赋值绑定（用于推导路由 Group 前缀等）
type UnifiedVarBinding struct {
	VarName  string           `json:"var_name"`
	CallSite *UnifiedCallSite `json:"call_site,omitempty"`
	Literal  string           `json:"literal,omitempty"`
}

// UnifiedEndpoint 端点（框架适配器产出）
type UnifiedEndpoint struct {
	Kind                string   `json:"kind"` // http_route | grpc_method | queue_consumer | cron_job
	Path                string   `json:"path"`
	Method              string   `json:"method"`
	AuthRequired        bool     `json:"auth_required"`
	AuthzPolicy         string   `json:"authz_policy,omitempty"`
	RateLimit           string   `json:"rate_limit,omitempty"`
	ProducesContentType []string `json:"produces_content_type,omitempty"`
	ConsumesContentType []string `json:"consumes_content_type,omitempty"`
	Framework           string   `json:"framework"`
	HandlerFunc         string   `json:"handler_func"`
	File                string   `json:"file"`
	Line                int      `json:"line"`
}

// UnifiedVariable 变量
type UnifiedVariable struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	DefaultValue string `json:"default_value,omitempty"`
	IsExported   bool   `json:"is_exported"`
}

// UnifiedConstant 常量
type UnifiedConstant struct {
	Name       string `json:"name"`
	Type       string `json:"type,omitempty"`
	Value      string `json:"value"`
	IsExported bool   `json:"is_exported"`
}

// UnifiedSink 危险Sink（框架适配器产出）
type UnifiedSink struct {
	Kind       string   `json:"kind"`
	Function   string   `json:"function"`
	Sanitizers []string `json:"sanitizers,omitempty"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
}

// AuthCheckInfo 认证检查信息（框架适配器产出）
type AuthCheckInfo struct {
	FuncName string `json:"func_name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"` // auth | authz
}
