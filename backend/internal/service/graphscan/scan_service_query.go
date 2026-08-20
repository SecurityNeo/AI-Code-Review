package graphscan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/model"
)

// GetGraphVisualization 获取图可视化数据（支持分层加载）
func (s *ScanService) GetGraphVisualization(projectID uint64, detailLevel, focusFile, focusPackage string, maxNodes int) (map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return map[string]interface{}{
			"status": "none",
			"nodes":  []interface{}{},
			"edges":  []interface{}{},
		}, nil
	}

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	var nodes []map[string]interface{}
	var edges []map[string]interface{}
	warning := ""

	switch detailLevel {
	case "low":
		// 包级聚合：每个包一个节点
		pkgNodes := make(map[string]map[string]interface{})
		for _, node := range g.AllNodes() {
			pkg := node.Package
			if pkg == "" {
				pkg = "(global)"
			}
			if _, ok := pkgNodes[pkg]; !ok {
				pkgNodes[pkg] = map[string]interface{}{
					"id":    fmt.Sprintf("pkg:%s", pkg),
					"type":  "package",
					"name":  pkg,
					"label": pkg,
					"count": 0,
				}
			}
			pkgNodes[pkg]["count"] = pkgNodes[pkg]["count"].(int) + 1
		}
		for _, n := range pkgNodes {
			nodes = append(nodes, n)
		}

		// 包间调用关系：按包聚合
		// 预构建函数名 -> 包名映射（用于模糊匹配 target）
		funcPkgMap := make(map[string]string)
		for _, node := range g.AllNodes() {
			if node.Type == graph.NodeFunc && node.Name != "" {
				funcPkgMap[node.Name] = node.Package
			}
		}
		
		pkgRels := make(map[string]int)
		for _, rel := range g.GetAllRelations() {
			fromNode, _ := g.GetNode(rel.From)
			toNode, _ := g.GetNode(rel.To)
			
			// From 包名
			var fromPkg string
			if fromNode != nil {
				fromPkg = fromNode.Package
			} else {
				// 从 ID 或 Extra 中推断
				if rel.Extra != nil && rel.Extra["caller_file"] != "" {
					// 从文件路径推断包名（与 ingestAST 一致）
					parts := strings.Split(rel.Extra["caller_file"], "/")
					if len(parts) >= 2 {
						fromPkg = parts[len(parts)-2]
					}
				}
			}
			
			// To 包名
			var toPkg string
			if toNode != nil {
				toPkg = toNode.Package
			} else {
				// 尝试从 ID 解析，或模糊匹配函数名
				parts := strings.Split(rel.To, ":")
				if len(parts) >= 3 {
					candidate := parts[1]
					// 仅当候选包名是简单标识符（不含路径分隔符/扩展名）时才使用
					if !strings.Contains(candidate, "/") && !strings.Contains(candidate, ".") {
						toPkg = candidate
					}
				}
				if toPkg == "" {
					// 尝试用函数名查找所属包
					for _, node := range g.AllNodes() {
						if node.Type == graph.NodeFunc && node.Name != "" {
							if strings.HasSuffix(rel.To, ":"+node.Name) || rel.To == "func:"+node.Name {
								toPkg = node.Package
								break
							}
						}
					}
				}
			}
			
			if fromPkg == "" {
				fromPkg = "(global)"
			}
			if toPkg == "" {
				toPkg = "(global)"
			}
			if fromPkg == toPkg {
				continue
			}
			key := fmt.Sprintf("%s|%s", fromPkg, toPkg)
			pkgRels[key] = pkgRels[key] + 1
		}
		for key, weight := range pkgRels {
			parts := strings.Split(key, "|")
			if len(parts) == 2 {
				edges = append(edges, map[string]interface{}{
					"from":   fmt.Sprintf("pkg:%s", parts[0]),
					"to":     fmt.Sprintf("pkg:%s", parts[1]),
					"type":   "calls",
					"weight": weight,
				})
			}
		}

	case "medium":
		// 文件级 + exported 函数 + 端点
		fileNodes := make(map[string]map[string]interface{})
		// 原始 node ID -> 新 id 的映射（用于 edges 引用修复）
		nodeIDMap := make(map[string]string)
		for _, node := range g.AllNodes() {
			file := node.File
			if file == "" {
				continue
			}
			// 聚焦筛选：只包含 focusFile 或 focusPackage 的节点
			if focusFile != "" && node.File != focusFile {
				continue
			}
			if focusPackage != "" && node.Package != focusPackage {
				continue
			}

			// 只保留 exported 函数、类型、端点、sink（排除 field 和 TODO）
			if node.Type == graph.NodeFunc && !node.IsExported {
				continue
			}
			if node.Type == graph.NodeField || node.Type == graph.NodeTODO {
				continue
			}

			id := fmt.Sprintf("file-node:%s:%s", file, node.ID)
			fileNodes[node.ID] = map[string]interface{}{
				"id":           id,
				"original_id":  node.ID,
				"type":         node.Type,
				"name":         node.Name,
				"label":        node.Name,
				"file":         node.File,
				"package":      node.Package,
				"line":         node.Location.LineStart,
				"role":         getStringProp(node.Properties, "security_role"),
				"complexity":   getIntProp(node.Properties, "complexity"),
			}
			nodeIDMap[node.ID] = id
		}
		for _, n := range fileNodes {
			nodes = append(nodes, n)
		}

		// 关系只保留文件间调用（使用映射后的新 ID，支持模糊匹配）
		// 预构建函数名 -> 节点ID 映射（用于跨文件调用模糊匹配）
		funcNameToNodeID := make(map[string]string)
		for _, node := range g.AllNodes() {
			if node.Type == graph.NodeFunc && node.Name != "" {
				funcNameToNodeID[node.Name] = node.ID
			}
		}
		
		for _, rel := range g.GetAllRelations() {
			if rel.Type != graph.RelCalls {
				continue
			}
			newFromID, fromOK := nodeIDMap[rel.From]
			newToID, toOK := nodeIDMap[rel.To]
			
			// From 模糊匹配：如果精确匹配失败，尝试按函数名查找
			if !fromOK {
				// 从 relation.From 解析函数名
				parts := strings.Split(rel.From, ":")
				if len(parts) >= 3 {
					funcName := parts[len(parts)-1]
					// 去掉 receiver 前缀
					if dotIdx := strings.LastIndex(funcName, "."); dotIdx >= 0 {
						funcName = funcName[dotIdx+1:]
					}
					if nodeID, ok := funcNameToNodeID[funcName]; ok {
						newFromID = nodeIDMap[nodeID]
						fromOK = newFromID != ""
					}
				}
			}
			
			// To 模糊匹配
			if !toOK {
				parts := strings.Split(rel.To, ":")
				if len(parts) >= 2 {
					funcName := parts[len(parts)-1]
					if dotIdx := strings.LastIndex(funcName, "."); dotIdx >= 0 {
						funcName = funcName[dotIdx+1:]
					}
					if nodeID, ok := funcNameToNodeID[funcName]; ok {
						newToID = nodeIDMap[nodeID]
						toOK = newToID != ""
					}
				}
			}
			
			if fromOK && toOK {
				edges = append(edges, map[string]interface{}{
					"from": newFromID,
					"to":   newToID,
					"type": rel.Type,
				})
			}
		}

	case "high":
		// 全部节点（上限 maxNodes）
		count := 0
		for _, node := range g.AllNodes() {
			if count >= maxNodes {
				warning = fmt.Sprintf("too many nodes, truncated to %d", maxNodes)
				break
			}
			nodes = append(nodes, map[string]interface{}{
				"id":       node.ID,
				"type":     node.Type,
				"name":     node.Name,
				"label":    node.Name,
				"file":     node.File,
				"package":  node.Package,
				"line":     node.Location.LineStart,
				"role":     getStringProp(node.Properties, "security_role"),
				"is_exported": node.IsExported,
			})
			count++
		}

		for _, rel := range g.GetAllRelations() {
			if len(edges) >= maxNodes {
				break
			}
			edges = append(edges, map[string]interface{}{
				"from": rel.From,
				"to":   rel.To,
				"type": rel.Type,
			})
		}
	}

	return map[string]interface{}{
		"status":   "completed",
		"detail_level": detailLevel,
		"node_count":   len(nodes),
		"edge_count":   len(edges),
		"nodes":        nodes,
		"edges":        edges,
		"warning":      warning,
	}, nil
}

// GetNodeDetail 获取节点详情 + 出入关系列表
func (s *ScanService) GetNodeDetail(projectID uint64, nodeID string) (map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return nil, fmt.Errorf("graph not found for project %d", projectID)
	}

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	// 支持 pkg:xxx 聚合节点（visualization low 级别生成）
	if strings.HasPrefix(nodeID, "pkg:") {
		pkgName := strings.TrimPrefix(nodeID, "pkg:")
		if pkgName == "" || pkgName == "(global)" {
			pkgName = ""
		}
		var funcCount, typeCount, endpointCount int
		var funcNames []string
		for _, n := range g.AllNodes() {
			if n.Package == pkgName {
				switch n.Type {
				case graph.NodeFunc:
					funcCount++
					funcNames = append(funcNames, n.Name)
				case graph.NodeType:
					typeCount++
				case graph.NodeEndpoint:
					endpointCount++
				}
			}
		}
		return map[string]interface{}{
			"id":            nodeID,
			"type":          "package",
			"name":          pkgName,
			"function_count": funcCount,
			"type_count":     typeCount,
			"endpoint_count": endpointCount,
			"functions":      funcNames,
		}, nil
	}

	node, ok := g.GetNode(nodeID)
	if !ok {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	// 入关系
	var incoming []map[string]interface{}
	for _, rel := range g.GetAllRelations() {
		if rel.To == nodeID {
			fromNode, exists := g.GetNode(rel.From)
			if exists {
				incoming = append(incoming, map[string]interface{}{
					"type":   rel.Type,
					"from_id": rel.From,
					"from_name": fromNode.Name,
					"from_file": fromNode.File,
				})
			}
		}
	}

	// 出关系
	var outgoing []map[string]interface{}
	for _, rel := range g.GetAllRelations() {
		if rel.From == nodeID {
			toNode, exists := g.GetNode(rel.To)
			if exists {
				outgoing = append(outgoing, map[string]interface{}{
					"type":    rel.Type,
					"to_id":   rel.To,
					"to_name": toNode.Name,
					"to_file": toNode.File,
				})
			}
		}
	}

	return map[string]interface{}{
		"id":       node.ID,
		"type":     node.Type,
		"name":     node.Name,
		"file":     node.File,
		"line":     node.Location.LineStart,
		"package":  node.Package,
		"signature": node.Signature,
		"is_exported": node.IsExported,
		"properties": node.Properties,
		"incoming_relations": incoming,
		"outgoing_relations": outgoing,
	}, nil
}

// GetGraphFiles 获取文件列表及文件级指标
func (s *ScanService) GetGraphFiles(projectID uint64) ([]map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return []map[string]interface{}{}, nil
	}

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	// 按文件聚合指标
	fileStats := make(map[string]map[string]interface{})
	for _, node := range g.AllNodes() {
		if node.File == "" {
			continue
		}
		if _, ok := fileStats[node.File]; !ok {
			fileStats[node.File] = map[string]interface{}{
				"path":       node.File,
				"functions":  0,
				"types":      0,
				"endpoints":  0,
				"sinks":      0,
				"complexity": 0,
				"package":    node.Package,
			}
		}
		stats := fileStats[node.File]
		switch node.Type {
		case graph.NodeFunc:
			stats["functions"] = stats["functions"].(int) + 1
			stats["complexity"] = stats["complexity"].(int) + getIntProp(node.Properties, "complexity")
		case graph.NodeType:
			stats["types"] = stats["types"].(int) + 1
		case graph.NodeEndpoint:
			stats["endpoints"] = stats["endpoints"].(int) + 1
		case graph.NodeSink:
			stats["sinks"] = stats["sinks"].(int) + 1
		}
	}

	// 转换为切片并排序
	var files []map[string]interface{}
	for _, stats := range fileStats {
		files = append(files, stats)
	}

	// 排序：先看包名再看文件名
	sort.Slice(files, func(i, j int) bool {
		pi := getStringProp(files[i], "package")
		pj := getStringProp(files[j], "package")
		if pi != pj {
			return pi < pj
		}
		return getStringProp(files[i], "path") < getStringProp(files[j], "path")
	})

	return files, nil
}

// GetGraphMetrics 获取度量仪表盘数据
func (s *ScanService) GetGraphMetrics(projectID uint64) (map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return map[string]interface{}{"status": "none"}, nil
	}

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	// 1. 复杂度分布
	complexityDist := map[string]int{"1-5": 0, "6-10": 0, "11-15": 0, ">15": 0}
	var topComplexFuncs []map[string]interface{}
	var totalComplexity, maxComplexity int
	var maxComplexFunc map[string]interface{}

	// 2. 安全角色分布
	roleDist := make(map[string]int)

	// 3. 框架统计
	frameworkStats := make(map[string]int)

	// 4. 导出 API 统计
	var totalExported, exportedWithDocs int

	functionCount := 0

	for _, node := range g.AllNodes() {
		if node.Type == graph.NodeFunc {
			functionCount++
			cc := getIntProp(node.Properties, "complexity")
			totalComplexity += cc
			if cc > maxComplexity {
				maxComplexity = cc
				maxComplexFunc = map[string]interface{}{
					"name": node.Name,
					"file": node.File,
					"complexity": cc,
				}
			}

			// 复杂度分布
			switch {
			case cc <= 5:
				complexityDist["1-5"]++
			case cc <= 10:
				complexityDist["6-10"]++
			case cc <= 15:
				complexityDist["11-15"]++
			default:
				complexityDist[">15"]++
			}

			// Top 复杂度函数（降序）
			if cc >= 10 {
				topComplexFuncs = append(topComplexFuncs, map[string]interface{}{
					"name":       node.Name,
					"file":       node.File,
					"complexity": cc,
				})
			}

			// 安全角色
			role := getStringProp(node.Properties, "security_role")
			if role != "" {
				roleDist[role]++
			}

			// 导出 API 统计
			if node.IsExported {
				totalExported++
				doc := getStringProp(node.Properties, "doc_comment")
				if doc != "" {
					exportedWithDocs++
				}
			}
		}

		if node.Type == graph.NodeEndpoint {
			fw := getStringProp(node.Properties, "framework")
			if fw != "" {
				frameworkStats[fw]++
			}
		}
	}

	// 排序 Top 复杂度函数
	sort.Slice(topComplexFuncs, func(i, j int) bool {
		return topComplexFuncs[i]["complexity"].(int) > topComplexFuncs[j]["complexity"].(int)
	})
	if len(topComplexFuncs) > 10 {
		topComplexFuncs = topComplexFuncs[:10]
	}

	// API 健康度（使用 JSON 转换兼容反序列化后的类型）
	var apiHealthAvg int
	if meta := g.GetMetadata("api_health_avg_score"); meta != nil {
		apiHealthAvg = metadataToInt(meta)
	}

	// 污点统计
	var taintPathCount int
	if meta := g.GetMetadata("taint_path_count"); meta != nil {
		taintPathCount = metadataToInt(meta)
	}

	// 重复函数
	var dupCount int
	if meta := g.GetMetadata("duplicated_functions"); meta != nil {
		dupCount = len(metadataToSlice(meta))
	}

	// TODO 统计 + 文件级风险评分
	todoStats := make(map[string]int)         // 按类型
	fileTODOCount := make(map[string]int)     // 按文件
	fileComplexity := make(map[string]int)    // 按文件的总复杂度
	fileFuncCount := make(map[string]int)     // 按文件的函数数
	fileUnauthEndpoints := make(map[string]int) // 按文件的未认证端点数
	fileTaintInvolved := make(map[string]bool)  // 文件是否涉及污点

	// 先收集污点涉及的文件
	if meta := g.GetMetadata("taint_paths"); meta != nil {
		for _, item := range metadataToSlice(meta) {
			if sf, ok := item["source_file"].(string); ok && sf != "" {
				fileTaintInvolved[sf] = true
			}
			if sf, ok := item["sink_file"].(string); ok && sf != "" {
				fileTaintInvolved[sf] = true
			}
		}
	}

	for _, node := range g.AllNodes() {
		if node.Type == graph.NodeTODO {
			typ := getStringProp(node.Properties, "comment_type")
			if typ == "" {
				typ = "todo"
			}
			todoStats[typ]++
			fileTODOCount[node.File]++
		}
		if node.Type == graph.NodeFunc {
			cc := getIntProp(node.Properties, "complexity")
			fileComplexity[node.File] += cc
			fileFuncCount[node.File]++
		}
		if node.Type == graph.NodeEndpoint {
			if !getBoolProp(node.Properties, "auth") {
				fileUnauthEndpoints[node.File]++
			}
		}
	}

	// 构建风险热力图数据
	var riskHeatmap []map[string]interface{}
	filesSet := make(map[string]bool)
	for f := range fileComplexity { filesSet[f] = true }
	for f := range fileTODOCount { filesSet[f] = true }
	for f := range fileUnauthEndpoints { filesSet[f] = true }
	for f := range fileTaintInvolved { filesSet[f] = true }

	for f := range filesSet {
		// 风险维度计算（0-10 分制）
		complexityScore := 0
		if fc := fileFuncCount[f]; fc > 0 {
			avg := fileComplexity[f] / fc
			if avg > 15 {
				complexityScore = 10
			} else if avg > 10 {
				complexityScore = 7
			} else if avg > 5 {
				complexityScore = 4
			} else {
				complexityScore = 1
			}
		}
		todoScore := min(fileTODOCount[f]*2, 10)
		unauthScore := min(fileUnauthEndpoints[f]*3, 10)
		taintScore := 0
		if fileTaintInvolved[f] {
			taintScore = 8
		}
		// 综合风险分
		totalRisk := (complexityScore + todoScore + unauthScore + taintScore) / 4
		riskHeatmap = append(riskHeatmap, map[string]interface{}{
			"file":             f,
			"complexity_score": complexityScore,
			"todo_score":       todoScore,
			"unauth_score":     unauthScore,
			"taint_score":      taintScore,
			"total_risk":       totalRisk,
			"func_count":       fileFuncCount[f],
		})
	}
	// 按综合风险降序排列
	sort.Slice(riskHeatmap, func(i, j int) bool {
		return riskHeatmap[i]["total_risk"].(int) > riskHeatmap[j]["total_risk"].(int)
	})
	if len(riskHeatmap) > 20 {
		riskHeatmap = riskHeatmap[:20]
	}

	avgComplexity := 0
	if functionCount > 0 {
		avgComplexity = totalComplexity / functionCount
	}

	return map[string]interface{}{
		"status": "completed",
		"complexity_distribution": complexityDist,
		"top_complex_functions":   topComplexFuncs,
		"avg_complexity":          avgComplexity,
		"max_complexity_function": maxComplexFunc,
		"security_role_distribution": roleDist,
		"framework_stats":          frameworkStats,
		"api_health_avg_score":     apiHealthAvg,
		"exported_api_count":       totalExported,
		"exported_api_with_docs":   exportedWithDocs,
		"taint_path_count":         taintPathCount,
		"duplicated_function_groups": dupCount,
		"todo_stats": todoStats,
		"risk_heatmap": riskHeatmap,
	}, nil
}

// GetGraphDependencies 获取依赖网络数据
func (s *ScanService) GetGraphDependencies(projectID uint64) (map[string]interface{}, error) {
	if !s.storage.Exists(projectID) {
		return map[string]interface{}{
			"status":         "none",
			"internal_deps":  []interface{}{},
			"external_deps":  []interface{}{},
		}, nil
	}

	g, err := s.storage.Load(projectID)
	if err != nil {
		return nil, err
	}

	// 收集所有内部包名（出现在 graph 节点中的 package）
	internalPackages := make(map[string]bool)
	for _, node := range g.AllNodes() {
		if node.Package != "" {
			internalPackages[node.Package] = true
		}
	}

	// 内置库包名列表
	stdLibPackages := map[string]bool{
		"fmt": true, "os": true, "io": true, "net": true, "strings": true,
		"encoding": true, "crypto": true, "time": true, "sync": true, "context": true,
		"bytes": true, "bufio": true, "errors": true, "flag": true, "log": true,
		"math": true, "path": true, "regexp": true, "sort": true, "strconv": true,
		"sys": true, "runtime": true, "reflect": true, "unsafe": true, "builtin": true,
	}

	// 内部依赖：包 → 包
	internalDeps := make(map[string]map[string]int) // fromPkg -> toPkg -> count
	// 外部依赖：包路径 → 被引用次数
	externalDeps := make(map[string]int)

	for _, rel := range g.GetAllRelations() {
		if rel.Type != graph.RelCalls {
			continue
		}
		fromNode, _ := g.GetNode(rel.From)
		if fromNode == nil {
			continue
		}

		fromPkg := fromNode.Package
		if fromPkg == "" {
			continue
		}

		var toPkg string
		// 尝试从目标节点获取包名
		toNode, ok := g.GetNode(rel.To)
		if ok && toNode != nil && toNode.Package != "" {
			toPkg = toNode.Package
		} else if rel.To != "" {
			// 从 To ID 解析包名（func:pkg:FuncName 格式）
			// 仅当 parts[1] 是简单标识符（不含路径分隔符/扩展名）时才使用，
			// 避免非Go语言 fallback ID 中的文件路径被误判为包名
			parts := strings.Split(rel.To, ":")
			if len(parts) >= 3 {
				candidate := parts[1]
				if !strings.Contains(candidate, "/") && !strings.Contains(candidate, ".") {
					toPkg = candidate
				}
			}
		}

		if toPkg == "" || fromPkg == toPkg {
			continue
		}

		// 判断是否为外部依赖：不在内部包列表中，或者是标准库
		isExternal := !internalPackages[toPkg]
		if stdLibPackages[toPkg] {
			isExternal = true
		}

		if isExternal {
			externalDeps[toPkg]++
		} else {
			if internalDeps[fromPkg] == nil {
				internalDeps[fromPkg] = make(map[string]int)
			}
			internalDeps[fromPkg][toPkg]++
		}
	}

	// 转换为输出格式
	var internalList []map[string]interface{}
	for fromPkg, toPkgs := range internalDeps {
		for toPkg, count := range toPkgs {
			internalList = append(internalList, map[string]interface{}{
				"from_package": fromPkg,
				"to_package":   toPkg,
				"count":        count,
			})
		}
	}

	var externalList []map[string]interface{}
	for pkg, count := range externalDeps {
		externalList = append(externalList, map[string]interface{}{
			"package": pkg,
			"count":   count,
		})
	}

	// 排序
	sort.Slice(internalList, func(i, j int) bool {
		return internalList[i]["count"].(int) > internalList[j]["count"].(int)
	})
	sort.Slice(externalList, func(i, j int) bool {
		return externalList[i]["count"].(int) > externalList[j]["count"].(int)
	})

	return map[string]interface{}{
		"status":        "completed",
		"internal_deps": internalList,
		"external_deps": externalList,
	}, nil
}

// GetFileContent 读取指定文件的内容（用于右侧面板代码片段）
func (s *ScanService) GetFileContent(projectID uint64, filePath string) (string, error) {
	// 获取项目信息以确定分支
	var project model.Project
	if err := s.db.First(&project, projectID).Error; err != nil {
		return "", fmt.Errorf("project not found: %w", err)
	}
	
	branch := project.GraphBaseBranch
	if branch == "" {
		branch = "main"
	}
	
	// 构建文件绝对路径
	repoDir := s.repoMgr.GetRepoDir(uint(projectID), branch)
	absPath := filepath.Join(repoDir, filePath)
	
	// 安全检查：确保文件在 repoDir 内（防止路径遍历）
	cleanPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("invalid file path: %w", err)
	}
	cleanRepoDir, _ := filepath.Abs(repoDir)
	if !strings.HasPrefix(cleanPath, cleanRepoDir+string(filepath.Separator)) && cleanPath != cleanRepoDir {
		return "", fmt.Errorf("file path outside repo directory")
	}
	
	// 读取文件内容
	content, err := os.ReadFile(absPath)
	if err != nil {
		// 如果文件不存在，可能是工作区路径不同，尝试从 workspace 直接构建
		altPath := filepath.Join(s.workspace, "repos", fmt.Sprintf("project_%d", projectID), branch, filePath)
		content, err = os.ReadFile(altPath)
		if err != nil {
			return "", fmt.Errorf("file not found: %w", err)
		}
	}
	
	return string(content), nil
}

// GetGraphSecurity 获取安全分析数据（独立 API）
func (s *ScanService) GetGraphSecurity(projectID uint64) (map[string]interface{}, error) {
	// 复用 GetGraphOverview 的安全相关数据
	overview, err := s.GetGraphOverview(projectID)
	if err != nil {
		return nil, err
	}

	// 计算安全评分（0-100，越高越安全）
	var securityScore int
	authCoverage, _ := overview["auth_coverage"].(map[string]interface{})
	if authCoverage != nil {
		total, _ := authCoverage["total"].(int)
		protected, _ := authCoverage["protected"].(int)
		if total > 0 {
			securityScore = int(float64(protected) / float64(total) * 100)
		}
	}
	// 如果有未认证的高风险端点，扣分
	unprotected, _ := authCoverage["unprotected_high_risk"].([]map[string]interface{})
	if len(unprotected) > 0 {
		score := securityScore - len(unprotected)*5
		if score < 0 { score = 0 }
		securityScore = score
	}
	// 有污点路径再扣分
	taintCount, _ := overview["taint_path_count"].(int)
	if taintCount > 0 {
		score := securityScore - taintCount*10
		if score < 0 { score = 0 }
		securityScore = score
	}

	// 只提取安全相关的字段
	return map[string]interface{}{
		"status":                    overview["status"],
		"taint_paths":               overview["taint_paths"],
		"sink_infos":                overview["sink_infos"],
		"taint_path_count":          overview["taint_path_count"],
		"auth_coverage":             overview["auth_coverage"],
		"duplicated_functions":      overview["duplicated_functions"],
		"api_health_items":          overview["api_health_items"],
		"api_health_avg_score":      overview["api_health_avg_score"],
		"endpoint_count":            overview["endpoint_count"],
		"function_count":            overview["function_count"],
		"security_score":            securityScore,
		"unauth_endpoints":          unprotected,
	}, nil
}
