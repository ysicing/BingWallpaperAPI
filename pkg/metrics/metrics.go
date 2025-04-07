package metrics

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics 应用指标集合
type Metrics struct {
	// 错误计数器
	Errors *prometheus.CounterVec

	// 下载的图片大小
	ImageSize prometheus.Histogram

	// 存储耗时
	StorageDuration *prometheus.HistogramVec

	// 更新耗时
	UpdateDuration prometheus.Histogram

	// 更新成功次数
	UpdateSuccess prometheus.Counter

	// 更新跳过次数
	UpdateSkipped prometheus.Counter

	// 清理成功次数
	CleanupSuccess prometheus.Counter

	// API请求计数
	APIRequests *prometheus.CounterVec

	// API响应时间
	APILatency *prometheus.HistogramVec
}

// NewMetrics 创建新的指标收集器
func NewMetrics(namespace string) *Metrics {
	m := &Metrics{
		Errors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "errors_total",
				Help:      "错误次数",
			},
			[]string{"type"},
		),

		ImageSize: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "image_size_bytes",
				Help:      "下载的图片大小（字节）",
				Buckets:   prometheus.ExponentialBuckets(1024, 2, 10), // 从1KB到1MB的指数分布
			},
		),

		StorageDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "storage_duration_seconds",
				Help:      "存储操作耗时（秒）",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"type"},
		),

		UpdateDuration: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "update_duration_seconds",
				Help:      "壁纸更新耗时（秒）",
				Buckets:   prometheus.LinearBuckets(1, 5, 10), // 从1秒到46秒的线性分布
			},
		),

		UpdateSuccess: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "update_success_total",
				Help:      "壁纸更新成功次数",
			},
		),

		UpdateSkipped: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "update_skipped_total",
				Help:      "壁纸更新跳过次数",
			},
		),

		CleanupSuccess: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "cleanup_success_total",
				Help:      "本地图片清理成功次数",
			},
		),

		APIRequests: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "api_requests_total",
				Help:      "API请求次数",
			},
			[]string{"method", "path", "status"},
		),

		APILatency: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "api_latency_seconds",
				Help:      "API响应时间（秒）",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "path"},
		),
	}

	return m
}

// MetricsMiddleware Gin中间件，用于收集API指标
func MetricsMiddleware(metrics *Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// 继续处理请求
		c.Next()

		// 忽略健康检查端点的指标收集
		if c.Request.URL.Path == "/health" || c.Request.URL.Path == "/metrics" {
			return
		}

		// 计算延迟
		latency := time.Since(start).Seconds()

		// 记录请求
		metrics.APIRequests.WithLabelValues(
			c.Request.Method,
			c.Request.URL.Path,
			fmt.Sprintf("%d", c.Writer.Status()),
		).Inc()

		// 记录延迟
		metrics.APILatency.WithLabelValues(
			c.Request.Method,
			c.Request.URL.Path,
		).Observe(latency)
	}
}
