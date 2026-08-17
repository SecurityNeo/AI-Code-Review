package graph

// GraphStorage 图谱存储接口
type GraphStorage interface {
	Save(projectID uint64, graph *MemorySymbolGraph) error
	Load(projectID uint64) (*MemorySymbolGraph, error)
	Delete(projectID uint64) error
	Exists(projectID uint64) bool
}
