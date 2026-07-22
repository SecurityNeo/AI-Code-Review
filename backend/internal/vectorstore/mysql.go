package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"gorm.io/gorm"
)

// MySQLStore implements Store using MySQL JSON columns.
type MySQLStore struct {
	db *gorm.DB
}

// NewMySQLStore creates a new MySQL-backed vector store.
func NewMySQLStore(db *gorm.DB) Store {
	return &MySQLStore{db: db}
}

func (s *MySQLStore) tableName(entityType string) string {
	switch entityType {
	case "issue":
		return "review_issue_vectors"
	case "rule":
		return "review_rule_vectors"
	case "incubation":
		return "rule_incubation_vectors"
	default:
		return ""
	}
}

// Save persists a single vector.
func (s *MySQLStore) Save(ctx context.Context, item Item) error {
	tbl := s.tableName(item.Key.EntityType)
	if tbl == "" {
		return fmt.Errorf("unknown entity_type: %s", item.Key.EntityType)
	}

	vecJSON, err := json.Marshal(item.Vector)
	if err != nil {
		return err
	}

	// Upsert: delete old then insert new (simpler than raw ON DUPLICATE for GORM generic)
	_ = s.db.WithContext(ctx).Table(tbl).
		Where("entity_id = ? AND model_id = ?", item.Key.EntityID, item.Key.ModelID).
		Delete(nil)

	return s.db.WithContext(ctx).Table(tbl).Create(map[string]any{
		"entity_id": item.Key.EntityID,
		"model_id":  item.Key.ModelID,
		"vector":    string(vecJSON),
		"dimension": item.Dimension,
	}).Error
}

// SaveBatch persists multiple vectors.
func (s *MySQLStore) SaveBatch(ctx context.Context, items []Item) error {
	for _, it := range items {
		if err := s.Save(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

// Get retrieves a vector by key.
func (s *MySQLStore) Get(ctx context.Context, key Key) (Vector, error) {
	tbl := s.tableName(key.EntityType)
	if tbl == "" {
		return nil, fmt.Errorf("unknown entity_type: %s", key.EntityType)
	}

	var row struct {
		Vector string
	}
	if err := s.db.WithContext(ctx).Table(tbl).
		Select("vector").
		Where("entity_id = ? AND model_id = ?", key.EntityID, key.ModelID).
		Scan(&row).Error; err != nil {
		return nil, err
	}
	if row.Vector == "" {
		return nil, ErrNotFound
	}
	return parseVector(row.Vector)
}

// Search performs an in-memory cosine-similarity scan over MySQL rows.
func (s *MySQLStore) Search(ctx context.Context, query Vector, opts SearchOpts) ([]Result, error) {
	if opts.Filters == nil {
		opts.Filters = map[string]any{}
	}
	entityType, _ := opts.Filters["entity_type"].(string)
	modelID, _ := opts.Filters["model_id"].(uint)

	var tables []string
	if entityType != "" {
		tbl := s.tableName(entityType)
		if tbl == "" {
			return nil, fmt.Errorf("unknown entity_type: %s", entityType)
		}
		tables = append(tables, tbl)
	} else {
		tables = []string{"review_issue_vectors", "review_rule_vectors", "rule_incubation_vectors"}
	}

	type rawRow struct {
		EntityType string `gorm:"column:entity_type"` // not a real column, populated manually
		EntityID   uint
		ModelID    uint
		VectorJSON string `gorm:"column:vector"`
	}

	var allRows []rawRow
	for _, tbl := range tables {
		db := s.db.WithContext(ctx).Table(tbl).Select("entity_id, model_id, vector")
		if modelID > 0 {
			db = db.Where("model_id = ?", modelID)
		}
		var rows []rawRow
		if err := db.Scan(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].EntityType = entityTypeFromTable(tbl)
		}
		allRows = append(allRows, rows...)
	}

	var results []Result
	for _, r := range allRows {
		vec, err := parseVector(r.VectorJSON)
		if err != nil {
			continue
		}
		score := cosineSimilarity(query, vec)
		if score >= opts.MinScore {
			results = append(results, Result{
				Key: Key{
					EntityType: r.EntityType,
					EntityID:   r.EntityID,
					ModelID:    r.ModelID,
				},
				Score:    score,
				Distance: 1 - score,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if opts.TopK > 0 && len(results) > opts.TopK {
		results = results[:opts.TopK]
	}
	return results, nil
}

// Delete removes a vector.
func (s *MySQLStore) Delete(ctx context.Context, key Key) error {
	tbl := s.tableName(key.EntityType)
	if tbl == "" {
		return fmt.Errorf("unknown entity_type: %s", key.EntityType)
	}
	return s.db.WithContext(ctx).Table(tbl).
		Where("entity_id = ? AND model_id = ?", key.EntityID, key.ModelID).
		Delete(nil).Error
}

// DeleteByEntityType drops all vectors of a given type.
func (s *MySQLStore) DeleteByEntityType(ctx context.Context, entityType string) error {
	tbl := s.tableName(entityType)
	if tbl == "" {
		return fmt.Errorf("unknown entity_type: %s", entityType)
	}
	return s.db.WithContext(ctx).Table(tbl).Where("1=1").Delete(nil).Error
}

// DeleteByModel drops all vectors produced by a given embedding model.
func (s *MySQLStore) DeleteByModel(ctx context.Context, modelID uint) error {
	for _, tbl := range []string{"review_issue_vectors", "review_rule_vectors", "rule_incubation_vectors"} {
		if err := s.db.WithContext(ctx).Table(tbl).Where("model_id = ?", modelID).Delete(nil).Error; err != nil {
			return err
		}
	}
	return nil
}

// Ping checks connectivity.
func (s *MySQLStore) Ping(_ context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

func parseVector(s string) (Vector, error) {
	var vec []float64
	if err := json.Unmarshal([]byte(s), &vec); err != nil {
		return nil, err
	}
	v := make(Vector, len(vec))
	for i, f := range vec {
		v[i] = float32(f)
	}
	return v, nil
}

func cosineSimilarity(a, b Vector) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func entityTypeFromTable(tbl string) string {
	switch tbl {
	case "review_issue_vectors":
		return "issue"
	case "review_rule_vectors":
		return "rule"
	case "rule_incubation_vectors":
		return "incubation"
	default:
		return ""
	}
}

var _ Store = (*MySQLStore)(nil)
