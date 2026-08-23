package mcp

import (
	"context"
	"time"
)

type authContextKey struct{}

// WithAuthContext 将 AuthContext 注入 context
func WithAuthContext(ctx context.Context, authCtx *AuthContext) context.Context {
	return context.WithValue(ctx, authContextKey{}, authCtx)
}

// GetAuthContext 从 context 获取 AuthContext
func GetAuthContext(ctx context.Context) *AuthContext {
	if v, ok := ctx.Value(authContextKey{}).(*AuthContext); ok {
		return v
	}
	return nil
}

// RateLimiter 简单的内存限流器
type RateLimiter struct {
	requests map[string][]time.Time // key -> timestamps
	limit    int
	window   time.Duration
}

// NewRateLimiter 创建限流器（默认 100 req/min）
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 100
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

// Allow 检查是否允许请求
func (rl *RateLimiter) Allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-rl.window)

	// 清理过期记录
	timestamps := rl.requests[key]
	var valid []time.Time
	for _, t := range timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	// 检查是否超限
	if len(valid) >= rl.limit {
		rl.requests[key] = valid
		return false
	}

	valid = append(valid, now)
	rl.requests[key] = valid
	return true
}
