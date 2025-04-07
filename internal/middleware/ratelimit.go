package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"
	
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// ClientRateLimiter 定义客户端速率限制器
type ClientRateLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitConfig 速率限制配置
type RateLimitConfig struct {
	// 每个窗口允许的请求数
	Limit int
	// 限制窗口，如1分钟
	WindowSize time.Duration
	// 清理过期客户端的间隔
	CleanupInterval time.Duration
	// 客户端视为过期的时间
	ExpiryDuration time.Duration
}

// RateLimiterMiddleware 创建速率限制中间件
func RateLimiterMiddleware(cfg RateLimitConfig, logger *zap.SugaredLogger) gin.HandlerFunc {
	// 默认清理间隔：5分钟
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}
	
	// 默认过期时间：1小时
	if cfg.ExpiryDuration == 0 {
		cfg.ExpiryDuration = time.Hour
	}
	
	// 创建限制器映射
	var (
		limiters = make(map[string]*ClientRateLimiter)
		mu       sync.RWMutex
	)
	
	// 启动定期清理过期限制器的协程
	go func() {
		ticker := time.NewTicker(cfg.CleanupInterval)
		defer ticker.Stop()
		
		for range ticker.C {
			mu.Lock()
			expiredCount := 0
			now := time.Now()
			
			for ip, client := range limiters {
				if now.Sub(client.lastSeen) > cfg.ExpiryDuration {
					delete(limiters, ip)
					expiredCount++
				}
			}
			
			if expiredCount > 0 {
				logger.Debugf("已清理 %d 个过期的速率限制器", expiredCount)
			}
			
			mu.Unlock()
		}
	}()
	
	// 返回中间件函数
	return func(c *gin.Context) {
		// 获取客户端IP
		clientIP := c.ClientIP()
		
		// 获取或创建限制器
		mu.RLock()
		limiter, exists := limiters[clientIP]
		mu.RUnlock()
		
		if !exists {
			limiterRate := rate.Every(cfg.WindowSize / time.Duration(cfg.Limit))
			limiter = &ClientRateLimiter{
				limiter:  rate.NewLimiter(rate.Limit(limiterRate), cfg.Limit),
				lastSeen: time.Now(),
			}
			
			mu.Lock()
			limiters[clientIP] = limiter
			mu.Unlock()
		} else {
			// 更新最后访问时间
			mu.Lock()
			limiter.lastSeen = time.Now()
			mu.Unlock()
		}
		
		// 检查速率限制
		if !limiter.limiter.Allow() {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "请求频率过高，请稍后再试",
				"retry_after": 60,
			})
			c.Abort()
			
			logger.Warnf("客户端 %s 已超过速率限制", clientIP)
			return
		}
		
		c.Next()
	}
}

// IPRateGroup 定义不同IP组的速率限制
type IPRateGroup struct {
	Name      string
	IPs       []string
	RateLimit int
	Matcher   func(ip string) bool
}

// NewGroupedRateLimiter 创建支持不同IP组速率限制的中间件
func NewGroupedRateLimiter(windowSize time.Duration, defaultLimit int, groups []IPRateGroup, logger *zap.SugaredLogger) gin.HandlerFunc {
	// 创建IP组映射
	ipGroups := make(map[string]*IPRateGroup)
	for i := range groups {
		group := &groups[i]
		for _, ip := range group.IPs {
			ipGroups[ip] = group
		}
	}
	
	// 创建限制器映射和互斥锁
	limiters := make(map[string]*rate.Limiter)
	var mu sync.RWMutex
	
	// 返回中间件函数
	return func(c *gin.Context) {
		// 获取客户端IP
		clientIP := c.ClientIP()
		
		// 确定IP组和限制
		var limit int
		limit = defaultLimit
		
		// 检查是否在特定组中
		if group, exists := ipGroups[clientIP]; exists {
			limit = group.RateLimit
		} else {
			// 查找匹配函数
			for _, group := range groups {
				if group.Matcher != nil && group.Matcher(clientIP) {
					limit = group.RateLimit
					break
				}
			}
		}
		
		// 计算速率
		limiterKey := fmt.Sprintf("%s:%d", clientIP, limit)
		
		// 获取或创建限制器
		mu.RLock()
		limiter, exists := limiters[limiterKey]
		mu.RUnlock()
		
		if !exists {
			// 创建新的限制器
			limiter = rate.NewLimiter(rate.Every(windowSize/time.Duration(limit)), limit)
			
			mu.Lock()
			limiters[limiterKey] = limiter
			mu.Unlock()
		}
		
		// 检查速率限制
		if !limiter.Allow() {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "请求频率过高，请稍后再试",
				"retry_after": 60,
			})
			c.Abort()
			
			logger.Warnf("客户端 %s 已超过速率限制 (限制: %d)", clientIP, limit)
			return
		}
		
		c.Next()
	}
}