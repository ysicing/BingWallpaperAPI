package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/ysicing/BingWallpaperAPI/config"
	"github.com/ysicing/BingWallpaperAPI/internal/bing"
)

// Handler API处理器
type Handler struct {
	cfg         *config.Config
	bingService *bing.Service
}

// NewHandler 创建新的API处理器
func NewHandler(cfg *config.Config, bingService *bing.Service) *Handler {
	return &Handler{
		cfg:         cfg,
		bingService: bingService,
	}
}

// GetWallpaper 获取壁纸信息
func (h *Handler) GetWallpaper(c *gin.Context) {
	metadata, err := h.bingService.GetCurrentWallpaper()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到壁纸"})
			return
		}
		logrus.Errorf("获取壁纸元数据失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	// 获取请求的主机名和协议
	host := c.Request.Host
	protocol := "http"
	if c.Request.TLS != nil {
		protocol = "https"
	}

	// 动态生成local_url
	dynamicLocalURL := fmt.Sprintf("%s://%s/images/%s", protocol, host, metadata.Storage.Filename)

	response := gin.H{
		"image_urls": gin.H{
			"local":    dynamicLocalURL,
			"object":   metadata.Storage.ObjectURL,
			"original": metadata.Storage.OriginalURL,
		},
		"metadata":   metadata.BingResponse.Images[0],
		"updated_at": metadata.Storage.LastUpdated,
	}

	c.JSON(http.StatusOK, response)
}

// GetMetadata 获取壁纸元数据
func (h *Handler) GetMetadata(c *gin.Context) {
	metadata, err := h.bingService.GetCurrentWallpaper()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到元数据"})
			return
		}
		logrus.Errorf("读取元数据失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	// 获取请求的主机名和协议
	host := c.Request.Host
	protocol := "http"
	if c.Request.TLS != nil {
		protocol = "https"
	}

	// 动态生成local_url
	dynamicLocalURL := fmt.Sprintf("%s://%s/images/%s", protocol, host, metadata.Storage.Filename)

	// 创建元数据的副本，并更新local_url
	metadataCopy := *metadata
	metadataCopy.Storage.LocalURL = dynamicLocalURL

	c.Header("Content-Type", "application/json")
	c.Header("Cache-Control", "public, max-age=3600")
	c.JSON(http.StatusOK, metadataCopy)
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
		logrus.Errorf("更新壁纸失败: %v", err)
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
	v1 := router.Group(h.cfg.API.BasePath)
	{
		v1.GET("/wallpaper", h.GetWallpaper)
		v1.GET("/update", h.UpdateWallpaper)
	}

	// 添加静态文件服务，用于提供图片文件
	// 设置适当的缓存控制头
	router.Static("/images", h.cfg.Storage.Local.Path)

	// 添加元数据和健康检查路由
	router.GET("/metadata", h.GetMetadata)
	router.GET("/health", h.HealthCheck)

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "资源不存在"})
	})
}
