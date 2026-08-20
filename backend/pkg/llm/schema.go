package llm

// AIReviewResult LLM 结构化评审输出（Strict Mode，全字段必填）
// 注意：strict=true 时所有字段均为 required，不能省略
type AIReviewResult struct {
	SchemaVersion       string               `json:"schema_version"`
	TotalScore          int                  `json:"total_score"`
	OriginalTotalScore  int                  `json:"original_total_score"`    // LLM 原始总分（后置校验前，用于展示对比）
	Dimensions          map[string]Dimension `json:"dimensions"`
	Summary             string               `json:"summary"`
	Issues              []AIReviewIssue      `json:"issues"`
	Recommendations     []string             `json:"recommendations"`

	// Phase 2 新增扩展字段（omitempty 兼容旧 API）
	SecurityFindings  []SecurityFinding `json:"security_findings,omitempty"`  // 安全发现独立区域
	TestingNotes      []TestingNote     `json:"testing_notes,omitempty"`      // 测试建议独立区域
	ImpactNotes       []ImpactNote      `json:"impact_notes,omitempty"`       // 影响分析独立区域
	DedupLog          []DedupEntry      `json:"dedup_log,omitempty"`          // 去重审计日志
	OverallSuggestion string            `json:"overall_suggestion,omitempty"` // 总体综合建议
}

// SecurityFinding 安全发现（Agent + LLM 共同确认）
type SecurityFinding struct {
	Source      string                 `json:"source"`      // "secret_scan" / "security_audit" / "llm_enhanced"
	Category    string                 `json:"category"`    // "credential_leak" / "sql_injection" / ...
	Severity    string                 `json:"severity"`    // critical/high/medium/low/info
	Confidence  float64                `json:"confidence"`  // 0.0-1.0，LLM 验证后的置信度
	File        string                 `json:"file"`
	LineStart   int                    `json:"line_start"`
	LineEnd     int                    `json:"line_end"`
	Title       string                 `json:"title"`       // 一句话描述
	Description string                 `json:"description"` // 详细描述
	CodeSnippet string                 `json:"code_snippet"` // 相关代码片段
	Suggestion  string                 `json:"suggestion"`  // 修复建议
	AgentMeta   map[string]interface{} `json:"agent_meta,omitempty"` // Agent 原始数据
}

// TestingNote 测试建议（结构化）
type TestingNote struct {
	FunctionName string         `json:"function_name"`
	FilePath     string         `json:"file_path"`
	LineNumber   int            `json:"line_number"`
	Scenarios    []TestScenario `json:"scenarios"`
}

// TestScenario 单个测试场景（LLM 生成）
type TestScenario struct {
	Name       string `json:"name"`       // "测试空用户场景"
	Input      string `json:"input"`      // "user == nil"
	Expected   string `json:"expected"`   // "返回 ErrUserNotFound"
	Reasoning  string `json:"reasoning"`  // "当前实现第45行未处理 nil"
	Priority   string `json:"priority"`   // "must_have" / "should_have" / "nice_to_have"
	LineNumber int    `json:"line_number"` // 对应代码行号
}

// ImpactNote 变更影响分析（结构化）
type ImpactNote struct {
	Type           string   `json:"type"`            // "breaking_change" / "schema_change" / "config_change" / "migration" / "behavior_change"
	FilePath       string   `json:"file_path"`
	SymbolName     string   `json:"symbol_name"`
	Description    string   `json:"description"`
	Severity       string   `json:"severity"`        // critical/high/medium/low
	Suggestion     string   `json:"suggestion"`
	AffectedFiles  []string `json:"affected_files"`  // 受影响文件列表
	Compatibility  string   `json:"compatibility"`   // "breaking" / "compatible" / "behavioral"
	MigrationSteps string   `json:"migration_steps"` // 迁移步骤（LLM 生成）
}

// DedupEntry 去重操作审计日志（review_arbitration 内部记录）
type DedupEntry struct {
	Action    string `json:"action"`         // "merged" / "kept_agent" / "kept_llm"
	AgentFile string `json:"agent_file"`
	AgentLine int    `json:"agent_line"`
	AgentMsg  string `json:"agent_message"`
	LLMFile   string `json:"llm_file"`
	LLMLine   int    `json:"llm_line"`
	LLMMsg    string `json:"llm_message"`
	Reason    string `json:"reason"`
}

// Dimension 维度评分
type Dimension struct {
	Score  int `json:"score"`
	Weight int `json:"weight"`
}

// AIReviewIssue 评审发现的 Issue
// Phase 2 新增 Source 和 AgentMeta 字段，用于标记问题来源（agent/llm/merged）
type AIReviewIssue struct {
	RuleCode    string                 `json:"rule_code"`     // 为空字符串表示不属于已知规则
	Severity    string                 `json:"severity"`      // critical/high/medium/low/info
	DeductScore int                    `json:"deduct_score"`  // 该 Issue 扣多少分
	Category    string                 `json:"category"`      // 维度分类
	File        string                 `json:"file"`          // 文件路径
	LineStart   int                    `json:"line_start"`    // 起始行号，不确定时为 0
	LineEnd     int                    `json:"line_end"`      // 结束行号，单行为 0
	CodeSnippet string                 `json:"code_snippet"`  // 相关代码片段
	Message     string                 `json:"message"`       // 问题描述
	Suggestion  string                 `json:"suggestion"`    // 改进建议
	Source      string                 `json:"source"`        // "agent" / "llm" / "merged" / ""
	AgentMeta   map[string]interface{} `json:"agent_meta,omitempty"` // Agent 原始元数据（仅 source=agent/merged 时存在）
}

// BatchReviewResult 分批评审收集模式的简化输出结构
// 只包含 issues[] 和 recommendations[]，不包含 total_score 和 dimensions
type BatchReviewResult struct {
	BatchNotes      string          `json:"batch_notes"`
	Issues          []AIReviewIssue `json:"issues"`
	Recommendations []string        `json:"recommendations"`
}

// GetBatchCollectionJSONSchema 返回分批评审收集模式的 JSON Schema
func GetBatchCollectionJSONSchema() interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"batch_notes", "issues", "recommendations"},
		"properties": map[string]interface{}{
			"batch_notes": map[string]interface{}{
				"type":        "string",
				"description": "对这批代码变更的简要评审摘要（50字以内）",
			},
			"issues": map[string]interface{}{
				"type":        "array",
				"description": "发现的问题列表，无问题填 []",
				"items": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"rule_code", "severity", "category", "file", "line_start", "line_end", "code_snippet", "message", "suggestion"},
					"properties": map[string]interface{}{
						"rule_code": map[string]interface{}{
							"type":        "string",
							"description": "规则编码，不属于已知规则填空字符串",
						},
						"severity": map[string]interface{}{
							"type":        "string",
							"description": "严重级别：critical/high/medium/low/info",
							"enum":        []string{"critical", "high", "medium", "low", "info"},
						},
						"category": map[string]interface{}{
							"type":        "string",
							"description": "所属维度 code",
						},
						"file": map[string]interface{}{
							"type":        "string",
							"description": "文件路径",
						},
						"line_start": map[string]interface{}{
							"type":        "integer",
							"description": "起始行号，不确定时填 0",
						},
						"line_end": map[string]interface{}{
							"type":        "integer",
							"description": "结束行号，单行为 0",
						},
						"code_snippet": map[string]interface{}{
							"type":        "string",
							"description": "相关代码片段",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "问题描述",
						},
						"suggestion": map[string]interface{}{
							"type":        "string",
							"description": "改进建议",
						},
					},
				},
			},
			"recommendations": map[string]interface{}{
				"type":        "array",
				"description": "改进建议列表，无建议填 []",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}
}

// GetReviewJSONSchema 返回动态 JSON Schema（根据模板实际维度生成）
func GetReviewJSONSchema(dimensions []string) interface{} {
	if len(dimensions) == 0 {
		dimensions = []string{"security", "code_quality", "readability", "maintainability", "test_coverage"}
	}

	dimProps := make(map[string]interface{})
	for _, d := range dimensions {
		dimProps[d] = map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"score", "weight"},
			"properties": map[string]interface{}{
				"score":  map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 100},
				"weight": map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 100},
			},
		}
	}

	return map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "total_score", "dimensions", "summary", "issues", "recommendations"},
		"properties": map[string]interface{}{
			"schema_version": map[string]interface{}{
				"type":        "string",
				"description": "Schema 版本，固定为 1.0",
			},
			"total_score": map[string]interface{}{
				"type":        "integer",
				"description": "综合评分 0-100",
				"minimum":     0,
				"maximum":     100,
			},
			"dimensions": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"required":             dimensions,
				"properties":           dimProps,
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "评审总结，100字以内",
			},
			"issues": map[string]interface{}{
				"type":        "array",
				"description": "发现的问题列表，无问题填 []",
				"items": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"rule_code", "severity", "deduct_score", "category", "file", "line_start", "line_end", "code_snippet", "message", "suggestion"},
					"properties": map[string]interface{}{
						"rule_code": map[string]interface{}{
							"type":        "string",
							"description": "规则编码，不属于已知规则填空字符串",
						},
						"severity": map[string]interface{}{
							"type":        "string",
							"description": "严重级别：critical/high/medium/low/info",
							"enum":        []string{"critical", "high", "medium", "low", "info"},
						},
						"deduct_score": map[string]interface{}{
							"type":        "integer",
							"description": "该 Issue 扣多少分",
							"minimum":     0,
							"maximum":     100,
						},
						"category": map[string]interface{}{
							"type":        "string",
							"description": "所属维度 code",
						},
						"file": map[string]interface{}{
							"type":        "string",
							"description": "文件路径",
						},
						"line_start": map[string]interface{}{
							"type":        "integer",
							"description": "起始行号，不确定时填 0",
						},
						"line_end": map[string]interface{}{
							"type":        "integer",
							"description": "结束行号，单行为 0",
						},
						"code_snippet": map[string]interface{}{
							"type":        "string",
							"description": "相关代码片段",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "问题描述",
						},
						"suggestion": map[string]interface{}{
							"type":        "string",
							"description": "改进建议",
						},
					},
				},
			},
			"recommendations": map[string]interface{}{
				"type":        "array",
				"description": "改进建议列表，无建议填 []",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}
}

// GetReviewArbitrationJSONSchema 返回 review_arbitration 专用的扩展 JSON Schema
// 与 GetReviewJSONSchema 相比，增加了 security_findings / testing_notes / impact_notes / dedup_log / overall_suggestion
func GetReviewArbitrationJSONSchema(dimensions []string) interface{} {
	// 1. 基础 Schema 沿用现有 GetReviewJSONSchema
	baseSchema := GetReviewJSONSchema(dimensions)
	schema, _ := baseSchema.(map[string]interface{})
	props, _ := schema["properties"].(map[string]interface{})
	itemsSchema, _ := props["issues"].(map[string]interface{})
	itemProps, _ := itemsSchema["items"].(map[string]interface{})
	itemProperties, _ := itemProps["properties"].(map[string]interface{})

	// 2. 扩展 Issue 字段（增加 source + agent_meta，不加入 required）
	itemProperties["source"] = map[string]interface{}{
		"type":        "string",
		"description": "问题来源：agent=规则引擎发现，llm=AI评审发现，merged=合并后",
		"enum":        []string{"agent", "llm", "merged", ""},
	}
	itemProperties["agent_meta"] = map[string]interface{}{
		"type":        "object",
		"description": "Agent 原始元数据（仅 source=agent/merged 时存在）",
	}

	// 3. 新增顶层字段
	props["security_findings"] = map[string]interface{}{
		"type":        "array",
		"description": "安全发现列表（仅展示用），无发现填 []",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"source", "category", "severity", "file", "line_start", "line_end", "title"},
			"properties": map[string]interface{}{
				"source":      map[string]interface{}{"type": "string"},
				"category":    map[string]interface{}{"type": "string"},
				"severity":    map[string]interface{}{"type": "string", "enum": []string{"critical", "high", "medium", "low", "info"}},
				"confidence":  map[string]interface{}{"type": "number", "minimum": 0, "maximum": 1},
				"file":        map[string]interface{}{"type": "string"},
				"line_start":  map[string]interface{}{"type": "integer"},
				"line_end":    map[string]interface{}{"type": "integer"},
				"title":       map[string]interface{}{"type": "string"},
				"description": map[string]interface{}{"type": "string"},
				"code_snippet": map[string]interface{}{"type": "string"},
				"suggestion":  map[string]interface{}{"type": "string"},
			},
		},
	}

	props["testing_notes"] = map[string]interface{}{
		"type":        "array",
		"description": "测试建议列表，无建议填 []",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"function_name", "file_path", "line_number", "scenarios"},
			"properties": map[string]interface{}{
				"function_name": map[string]interface{}{"type": "string"},
				"file_path":     map[string]interface{}{"type": "string"},
				"line_number":   map[string]interface{}{"type": "integer"},
				"scenarios": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"name", "input", "expected", "reasoning", "priority"},
						"properties": map[string]interface{}{
							"name":      map[string]interface{}{"type": "string"},
							"input":     map[string]interface{}{"type": "string"},
							"expected":  map[string]interface{}{"type": "string"},
							"reasoning": map[string]interface{}{"type": "string"},
							"priority":  map[string]interface{}{"type": "string", "enum": []string{"must_have", "should_have", "nice_to_have"}},
							"line_number": map[string]interface{}{"type": "integer"},
						},
					},
				},
			},
		},
	}

	props["impact_notes"] = map[string]interface{}{
		"type":        "array",
		"description": "影响分析列表，无分析填 []",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"type", "file_path", "description", "severity", "compatibility"},
			"properties": map[string]interface{}{
				"type":            map[string]interface{}{"type": "string", "enum": []string{"breaking_change", "behavior_change", "schema_change", "config_change", "migration"}},
				"file_path":       map[string]interface{}{"type": "string"},
				"symbol_name":     map[string]interface{}{"type": "string"},
				"description":     map[string]interface{}{"type": "string"},
				"severity":        map[string]interface{}{"type": "string", "enum": []string{"critical", "high", "medium", "low"}},
				"suggestion":      map[string]interface{}{"type": "string"},
				"affected_files":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"compatibility":   map[string]interface{}{"type": "string"},
				"migration_steps": map[string]interface{}{"type": "string"},
			},
		},
	}

	props["dedup_log"] = map[string]interface{}{
		"type":        "array",
		"description": "去重操作审计日志，无去重卡 []",
		"items": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action"},
			"properties": map[string]interface{}{
				"action":          map[string]interface{}{"type": "string", "enum": []string{"merged", "kept_agent", "kept_llm"}},
				"agent_file":      map[string]interface{}{"type": "string"},
				"agent_line":      map[string]interface{}{"type": "integer"},
				"agent_message":   map[string]interface{}{"type": "string"},
				"llm_file":        map[string]interface{}{"type": "string"},
				"llm_line":        map[string]interface{}{"type": "integer"},
				"llm_message":     map[string]interface{}{"type": "string"},
				"reason":          map[string]interface{}{"type": "string"},
			},
		},
	}

	props["overall_suggestion"] = map[string]interface{}{
		"type":        "string",
		"description": "基于全量问题的综合建议，500字以内",
	}

	// 4. 扩展 required 数组
	schema["required"] = []string{
		"schema_version", "total_score", "dimensions", "summary", "issues", "recommendations",
		"security_findings", "testing_notes", "impact_notes", "dedup_log", "overall_suggestion",
	}

	return schema
}
