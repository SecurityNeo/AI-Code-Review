package vectorstore

import (
	"context"
	"fmt"
)

// Vector is a generic float32 slice used for embeddings.
type Vector []float32

// Key uniquely identifies a stored vector.
type Key struct {
	EntityType string // "issue" | "rule" | "incubation"
	EntityID   uint
	ModelID    uint
}

// Item is a single vector entry to be stored.
type Item struct {
	Key       Key
	Vector    Vector
	Dimension int
}

// SearchOpts configures a similarity search.
type SearchOpts struct {
	TopK     int
	MinScore float64
	Filters  map[string]any // optional pre-filters (e.g. entity_type, model_id)
}

// Result represents one matched vector.
type Result struct {
	Key      Key
	Score    float64 // cosine similarity
	Distance float64 // 1 - Score
	Vector   Vector  // populated only when needed
}

// Store abstracts vector storage and retrieval.
type Store interface {
	Save(ctx context.Context, item Item) error
	SaveBatch(ctx context.Context, items []Item) error

	Get(ctx context.Context, key Key) (Vector, error)
	Search(ctx context.Context, query Vector, opts SearchOpts) ([]Result, error)

	Delete(ctx context.Context, key Key) error
	DeleteByEntityType(ctx context.Context, entityType string) error
	DeleteByModel(ctx context.Context, modelID uint) error

	Ping(ctx context.Context) error
}

// ErrNotFound is returned when a vector is not found.
var ErrNotFound = fmt.Errorf("vector not found")
