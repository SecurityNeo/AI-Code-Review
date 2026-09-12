package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// clientLimiter 基于客户端 IP 的固定窗口计数器
type clientLimiter struct {
	count  int
	window time.Time // 窗口起始时间
}

var (
	webhookRateMap = make(map[string]*clientLimiter)
	publicRateMap  = make(map[string]*clientLimiter)
	webhookMu      sync.Mutex
	publicMu       sync.Mutex
)

const (
	webhookLimit  = 100       // 100 req/min
	webhookWindow = time.Minute
	publicLimit   = 30        // 30 req/min
	publicWindow  = time.Minute
)

func init() {
	// ⚠️ 定期清理过期 IP 记录，防止内存无限增长
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cleanupRateMap(webhookRateMap, &webhookMu)
			cleanupRateMap(publicRateMap, &publicMu)
		}
	}()
}

// cleanupRateMap 清理超过 2 倍窗口时间的过期记录
func cleanupRateMap(m map[string]*clientLimiter, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	for ip, l := range m {
		if now.Sub(l.window) > 2*webhookWindow {
			delete(m, ip)
		}
	}
}

// rateLimitMiddleware 返回一个基于 clientIP 的固定窗口限流中间件
func rateLimitMiddleware(limit int, window time.Duration, rateMap map[string]*clientLimiter, mu *sync.Mutex) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()

		mu.Lock()
		l, exists := rateMap[ip]
		if !exists || now.Sub(l.window) >= window {
			// 进入新窗口，重置计数
			l = &clientLimiter{count: 0, window: now}
			rateMap[ip] = l
		}
		l.count++
		count := l.count
		mu.Unlock()

		if count > limit {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}

// WebhookRateLimit Webhook 接口限流：100 次/分钟
func WebhookRateLimit() gin.HandlerFunc {
	return rateLimitMiddleware(webhookLimit, webhookWindow, webhookRateMap, &webhookMu)
}

// PublicAPIRateLimit 公共 API 限流：30 次/分钟
func PublicAPIRateLimit() gin.HandlerFunc {
	return rateLimitMiddleware(publicLimit, publicWindow, publicRateMap, &publicMu)
}
