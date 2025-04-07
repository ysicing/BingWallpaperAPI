package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ysicing/BingWallpaperAPI/config"
	"github.com/ysicing/BingWallpaperAPI/internal/bing"
	"github.com/ysicing/BingWallpaperAPI/internal/logger"
	"go.uber.org/zap"
)

// Handler API处理器
type Handler struct {
	log         *zap.SugaredLogger
	cfg         *config.Config
	bingService *bing.Service
}

// NewHandler 创建新的API处理器
func NewHandler(cfg *config.Config, bingService *bing.Service) *Handler {
	return &Handler{
		log:         logger.GetLogger("api-handler"),
		cfg:         cfg,
		bingService: bingService,
	}
}

// GetWallpaper 获取壁纸信息（新的RESTful端点）
func (h *Handler) GetWallpaper(c *gin.Context) {
	metadata, err := h.bingService.GetCurrentWallpaper()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到壁纸"})
			return
		}

		h.log.Errorf("获取壁纸元数据失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	// 构建响应
	response := gin.H{
		"image_urls": gin.H{},
		"metadata":   metadata.BingResponse.Images[0],
		"updated_at": metadata.Storage.LastUpdated,
	}

	// 添加可用的URL
	urls := make(map[string]string)

	if metadata.Storage.LocalURL != "" {
		urls["local"] = metadata.Storage.LocalURL
	}

	if metadata.Storage.ObjectURL != "" {
		urls["object_storage"] = metadata.Storage.ObjectURL
	}

	if metadata.Storage.OriginalURL != "" {
		urls["original"] = metadata.Storage.OriginalURL
	}

	response["image_urls"] = urls

	c.JSON(http.StatusOK, response)
}

// GetWallpaperImage 获取壁纸图片（兼容旧API）
func (h *Handler) GetWallpaperImage(c *gin.Context) {
	metadata, err := h.bingService.GetCurrentWallpaper()
	if err != nil || metadata.Storage.LocalURL == "" {
		// 如果没有本地URL，检查是否可以重定向到对象存储
		if err == nil && metadata.Storage.ObjectURL != "" {
			c.Redirect(http.StatusTemporaryRedirect, metadata.Storage.ObjectURL)
			return
		}

		h.log.Warn("缓存中未找到壁纸图片")
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到壁纸"})
		return
	}

	// 构建本地文件路径
	filename := metadata.Storage.Filename

	// 检查文件是否存在
	localPath := h.cfg.Storage.Local.Path
	fullPath := filepath.Join(localPath, filename)

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		// 本地文件不存在，重定向到对象存储（如果有）
		if metadata.Storage.ObjectURL != "" {
			c.Redirect(http.StatusTemporaryRedirect, metadata.Storage.ObjectURL)
			return
		}

		h.log.Warn("本地文件不存在，且无对象存储URL")
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到壁纸"})
		return
	}

	// 设置响应头
	c.Header("Content-Type", metadata.Storage.ContentType)
	c.Header("Cache-Control", "public, max-age=86400") // 24小时缓存

	// 提供文件
	c.File(fullPath)
}

// GetMetadata 获取壁纸元数据
func (h *Handler) GetMetadata(c *gin.Context) {
	metadata, err := h.bingService.GetCurrentWallpaper()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到元数据"})
			return
		}

		h.log.Errorf("读取元数据失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	// 设置响应头
	c.Header("Content-Type", "application/json")
	c.Header("Cache-Control", "public, max-age=3600") // 1小时缓存

	c.JSON(http.StatusOK, metadata)
}

// HealthCheck 健康检查端点
func (h *Handler) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "OK",
		"version":   "1.0.0",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// UpdateWallpaper 手动触发壁纸更新
func (h *Handler) UpdateWallpaper(c *gin.Context) {
	updated, err := h.bingService.CheckAndUpdateWallpaper()
	if err != nil {
		h.log.Errorf("更新壁纸失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("更新壁纸失败: %v", err)})
		return
	}

	if !updated {
		c.JSON(http.StatusOK, gin.H{"message": "壁纸已是最新"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "壁纸已更新"})
}

// SetupRoutes 设置路由
func (h *Handler) SetupRoutes(router *gin.Engine) {
	// API版本路由（新的RESTful API）
	v1 := router.Group(h.cfg.API.BasePath)
	{
		v1.GET("/wallpaper", h.GetWallpaper)
		v1.POST("/wallpaper/update", h.UpdateWallpaper)
		v1.GET("/health", h.HealthCheck)
	}

	// 兼容旧API
	router.GET("/images", h.GetWallpaperImage)
	router.GET("/metadata", h.GetMetadata)
	router.GET("/health", h.HealthCheck)

	// 静态文件路由（如果有前端页面）
	router.Static("/static", "./static")

	// 添加404处理
	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "资源不存在"})
	})
}
