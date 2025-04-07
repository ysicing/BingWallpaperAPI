package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robfig/cron/v3"
	"github.com/ysicing/BingWallpaperAPI/config"
	"github.com/ysicing/BingWallpaperAPI/internal/api"
	"github.com/ysicing/BingWallpaperAPI/internal/bing"
	"github.com/ysicing/BingWallpaperAPI/internal/logger"
	"github.com/ysicing/BingWallpaperAPI/internal/middleware"
	"github.com/ysicing/BingWallpaperAPI/pkg/metrics"
	"github.com/ysicing/BingWallpaperAPI/pkg/storage"
	"go.uber.org/zap"
)

// 全局变量，用于优雅重启
var (
	scheduler      *cron.Cron
	cronEntryIDs   map[string]cron.EntryID
	cronMutex      sync.Mutex
	bingService    *bing.Service
	serviceOptions bing.ServiceOptions
)

func main() {
	// 初始化日志
	logger.Init()
	log := logger.GetLogger("main")
	defer logger.Sync()

	log.Info("正在启动必应壁纸API服务...")

	// 加载配置
	if err := config.LoadConfig(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	cfg := &config.AppConfig

	// 创建指标收集器
	metricsCollector := metrics.NewMetrics("bingwallpaper")

	// 初始化存储提供者
	var localStorageProvider storage.Provider
	var objectStorageProvider storage.Provider

	// 初始化cronEntryIDs
	cronEntryIDs = make(map[string]cron.EntryID)

	// 配置热重载回调
	configReloadCallback := func(newCfg *config.Config) {
		log.Info("检测到配置变更，正在重新加载服务...")

		// 重新初始化存储
		initStorage(newCfg, log, &localStorageProvider, &objectStorageProvider)

		// 更新服务配置
		if bingService != nil {
			bingService.UpdateConfig(newCfg)
		}

		// 更新定时任务
		updateCronJobs(newCfg, log)
	}

	// 启动配置监视
	config.WatchConfig(log, configReloadCallback)

	// 初始化存储
	initStorage(cfg, log, &localStorageProvider, &objectStorageProvider)

	// 创建必应服务选项
	serviceOptions = bing.ServiceOptions{
		Config:        cfg,
		LocalStorage:  localStorageProvider,
		ObjectStorage: objectStorageProvider,
		Metrics:       metricsCollector,
		HTTPTimeout:   30 * time.Second,
	}

	// 创建必应服务
	bingService = bing.NewService(serviceOptions)

	// 确保元数据目录存在
	metadataDir := filepath.Dir(cfg.Storage.MetadataPath)
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		log.Fatalf("创建元数据目录失败: %v", err)
	}

	// 初始化壁纸
	initWallpaper(log, bingService)

	// 设置定时任务
	scheduler = cron.New(cron.WithSeconds())
	setupCronJobs(cfg, log)

	// 启动定时任务
	scheduler.Start()
	defer scheduler.Stop()

	// 设置Gin模式
	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else if os.Getenv("GIN_MODE") == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建Gin路由
	router := setupRouter(cfg, log, bingService, metricsCollector)

	// 创建HTTP服务器
	server := &http.Server{
		Addr:    fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port),
		Handler: router,
	}

	// 优雅关闭
	shutdownServer := setupGracefulShutdown(server, log)

	// 启动服务器
	log.Infof("监听 %s:%s", cfg.Server.Host, cfg.Server.Port)
	log.Infof("API基础路径: %s", cfg.API.BasePath)
	log.Infof("壁纸访问地址: http://localhost:%s/images", cfg.Server.Port)
	log.Infof("API端点: http://localhost:%s%s/wallpaper", cfg.Server.Port, cfg.API.BasePath)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("启动服务器失败: %v", err)
	}

	// 等待关闭完成
	<-shutdownServer
	log.Info("服务器已完全关闭")
}

// initStorage 初始化存储提供者
func initStorage(cfg *config.Config, log *zap.SugaredLogger, localProvider *storage.Provider, objectProvider *storage.Provider) {
	// 如果启用了本地存储
	if cfg.Storage.Local.Enabled {
		basePath := cfg.Storage.Local.Path
		baseURL := cfg.Storage.Local.BaseURL

		// 确保本地存储目录存在
		if err := os.MkdirAll(basePath, 0755); err != nil {
			log.Fatalf("创建本地存储目录失败: %v", err)
		}

		// 创建或更新本地存储提供者
		localStorage, err := storage.NewLocalStorage(basePath, baseURL, logger.GetLogger("local-storage"))
		if err != nil {
			log.Fatalf("初始化本地存储失败: %v", err)
		}

		*localProvider = localStorage
		log.Infof("已初始化本地存储: %s", basePath)
	} else {
		*localProvider = nil
		log.Info("本地存储已禁用")
	}

	// 如果启用了对象存储
	if cfg.Storage.Object.Enabled {
		// 创建对象存储提供者
		objConfig := storage.ObjectStorageConfig{
			Endpoint:        cfg.Storage.Object.Endpoint,
			AccessKeyID:     cfg.Storage.Object.AccessKeyID,
			SecretAccessKey: cfg.Storage.Object.SecretAccessKey,
			BucketName:      cfg.Storage.Object.BucketName,
			Region:          cfg.Storage.Object.Region,
			UseSSL:          cfg.Storage.Object.UseSSL,
			BaseURL:         cfg.Storage.Object.BaseURL,
			PathPrefix:      cfg.Storage.Object.PathPrefix,
			ChunkSize:       5 * 1024 * 1024, // 默认5MB分片
			MaxRetries:      3,
		}

		objectStorage, err := storage.NewObjectStorage(objConfig, logger.GetLogger("object-storage"))
		if err != nil {
			log.Errorf("初始化对象存储失败: %v", err)
			log.Warn("将继续运行，但对象存储功能将不可用")
			*objectProvider = nil
		} else {
			*objectProvider = objectStorage
			log.Infof("已初始化对象存储: %s", objConfig.BucketName)
		}
	} else {
		*objectProvider = nil
		log.Info("对象存储已禁用")
	}

	// 确保有至少一个存储提供者
	if *localProvider == nil && *objectProvider == nil {
		log.Fatal("错误: 必须启用至少一个存储提供者 (本地或对象存储)")
	}
}

// initWallpaper 初始化壁纸
func initWallpaper(log *zap.SugaredLogger, bingService *bing.Service) {
	// 检查元数据文件是否存在
	_, err := bingService.GetCurrentWallpaper()
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("未找到缓存的壁纸元数据，正在下载...")
			if _, err := bingService.CheckAndUpdateWallpaper(); err != nil {
				log.Errorf("初始化壁纸下载失败: %v", err)
			}
		} else {
			log.Errorf("检查元数据失败: %v", err)
		}
	} else {
		log.Info("找到缓存的壁纸元数据")
	}
}

// setupCronJobs 设置定时任务
func setupCronJobs(cfg *config.Config, log *zap.SugaredLogger) {
	cronMutex.Lock()
	defer cronMutex.Unlock()

	// 定时更新壁纸
	spew.Dump(cfg.Schedule.CheckInterval)
	updateEntryID, err := scheduler.AddFunc(cfg.Schedule.CheckInterval, func() {
		log.Info("执行定时壁纸更新")
		if _, err := bingService.CheckAndUpdateWallpaper(); err != nil {
			log.Errorf("定时壁纸更新失败: %v", err)
		}
	})

	if err != nil {
		log.Fatalf("设置壁纸更新定时任务失败: %v", err)
	}
	cronEntryIDs["update"] = updateEntryID

	// 定时清理过期文件
	cleanupEntryID, err := scheduler.AddFunc(cfg.Schedule.CleanupInterval, func() {
		log.Info("执行定时清理过期文件")
		if err := bingService.CleanupOldFiles(); err != nil {
			log.Errorf("清理过期文件失败: %v", err)
		}
	})

	if err != nil {
		log.Fatalf("设置清理定时任务失败: %v", err)
	}
	cronEntryIDs["cleanup"] = cleanupEntryID
}

// updateCronJobs 更新定时任务
func updateCronJobs(cfg *config.Config, log *zap.SugaredLogger) {
	cronMutex.Lock()
	defer cronMutex.Unlock()

	// 移除现有任务
	if updateID, exists := cronEntryIDs["update"]; exists {
		scheduler.Remove(updateID)
	}

	if cleanupID, exists := cronEntryIDs["cleanup"]; exists {
		scheduler.Remove(cleanupID)
	}

	// 添加新任务
	updateEntryID, err := scheduler.AddFunc(cfg.Schedule.CheckInterval, func() {
		log.Info("执行定时壁纸更新")
		if _, err := bingService.CheckAndUpdateWallpaper(); err != nil {
			log.Errorf("定时壁纸更新失败: %v", err)
		}
	})

	if err != nil {
		log.Errorf("更新壁纸更新定时任务失败: %v", err)
	} else {
		cronEntryIDs["update"] = updateEntryID
	}

	cleanupEntryID, err := scheduler.AddFunc(cfg.Schedule.CleanupInterval, func() {
		log.Info("执行定时清理过期文件")
		if err := bingService.CleanupOldFiles(); err != nil {
			log.Errorf("清理过期文件失败: %v", err)
		}
	})

	if err != nil {
		log.Errorf("更新清理定时任务失败: %v", err)
	} else {
		cronEntryIDs["cleanup"] = cleanupEntryID
	}
}

// setupRouter 设置路由
func setupRouter(cfg *config.Config, log *zap.SugaredLogger, bingService *bing.Service, metricsCollector *metrics.Metrics) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())

	// 启用Gzip压缩
	router.Use(gzip.Gzip(gzip.DefaultCompression))

	// 添加CORS支持
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "HEAD"},
		AllowHeaders:     []string{"Origin", "Content-Type"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	// 添加指标中间件
	router.Use(metrics.MetricsMiddleware(metricsCollector))

	// 添加API速率限制
	if cfg.API.RateLimit > 0 {
		windowDuration := cfg.API.RateLimitWindow
		if windowDuration == 0 {
			windowDuration = time.Minute
		}

		rateLimitCfg := middleware.RateLimitConfig{
			Limit:           cfg.API.RateLimit,
			WindowSize:      windowDuration,
			CleanupInterval: 5 * time.Minute,
			ExpiryDuration:  time.Hour,
		}

		router.Use(middleware.RateLimiterMiddleware(rateLimitCfg, log))
	}

	// 在开发模式下启用pprof
	if os.Getenv("GIN_MODE") != "release" {
		pprof.Register(router)
		log.Info("已启用pprof分析器，路径: /debug/pprof")
	}

	// 添加Prometheus指标端点
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 设置API路由
	apiHandler := api.NewHandler(cfg, bingService)
	apiHandler.SetupRoutes(router)

	return router
}

// setupGracefulShutdown 设置优雅关闭
func setupGracefulShutdown(server *http.Server, log *zap.SugaredLogger) chan struct{} {
	done := make(chan struct{})

	go func() {
		// 监听中断信号
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		log.Info("正在关闭服务器...")

		// 创建一个带超时的上下文
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// 尝试优雅关闭
		if err := server.Shutdown(ctx); err != nil {
			log.Errorf("服务器关闭失败: %v", err)
		}

		log.Info("服务器已关闭")
		close(done)
	}()

	return done
}
