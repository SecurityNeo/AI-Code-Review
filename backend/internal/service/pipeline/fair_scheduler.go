package pipeline

import (
	"sync"
	"time"
)

// FairScheduler 按 org 加权的公平调度器（Token Bucket 实现）
// 多租户改造：防止单个 org 的任务耗尽全局资源，确保各组织的任务获得公平执行机会。
// 当前采用纯内存实现（无需外部依赖如 golang.org/x/time/rate），每个 org 独立计数。
type FairScheduler struct {
	mu      sync.RWMutex
	buckets map[uint]*tokenBucket // org_id → Token Bucket
}

type tokenBucket struct {
	tokens   float64   // 当前 token 数
	lastFill time.Time // 上次填充时间
	rate     float64   // 填充速率（token/s）
	burst    int       // 桶容量（突发上限）
}

// NewFairScheduler 创建公平调度器
// 多租户改造：单组织场景（org_id=1）默认 burst=3 足够大，行为与原全局 FIFO 基本一致。
func NewFairScheduler() *FairScheduler {
	return &FairScheduler{
		buckets: make(map[uint]*tokenBucket),
	}
}

// Allow 判断指定 org 当前是否允许执行
// 返回值 true 表示获得执行许可（已消耗 1 token）；false 表示被限流，应延后重试。
func (fs *FairScheduler) Allow(orgID uint) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	b, ok := fs.buckets[orgID]
	if !ok {
		// 默认值：每 org 每秒 1 个 token，突发上限 3
		// 单组织场景下 burst=3 通常足够，不会形成瓶颈
		b = &tokenBucket{
			tokens:   3.0,
			lastFill: time.Now(),
			rate:     1.0,
			burst:    3,
		}
		fs.buckets[orgID] = b
	}

	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > float64(b.burst) {
		b.tokens = float64(b.burst)
	}
	b.lastFill = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// SetOrgRate 为指定 org 设置自定义 rate/burst（管理接口用）
// rate: 每秒产生的 token 数；burst: 桶最大容量
func (fs *FairScheduler) SetOrgRate(orgID uint, rate float64, burst int) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if b, ok := fs.buckets[orgID]; ok {
		b.rate = rate
		b.burst = burst
	} else {
		fs.buckets[orgID] = &tokenBucket{
			tokens:   float64(burst),
			lastFill: time.Now(),
			rate:     rate,
			burst:    burst,
		}
	}
}
