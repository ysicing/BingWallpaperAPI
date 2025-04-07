package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ergoapi/util/exgin"
	"github.com/ergoapi/util/exhttp"
	"github.com/gin-contrib/gzip"
	"github.com/sirupsen/logrus"
	"github.com/ysicing/BingWallpaperAPI/config"
	"github.com/ysicing/BingWallpaperAPI/internal/api"
	"github.com/ysicing/BingWallpaperAPI/internal/bing"
	"github.com/ysicing/BingWallpaperAPI/internal/cron"
	"github.com/ysicing/BingWallpaperAPI/pkg/metrics"
	"github.com/ysicing/BingWallpaperAPI/pkg/storage"
)

var (
	bingService    *bing.Service
	serviceOptions bing.ServiceOptions
	scheduler      *cron.Scheduler
)

func init() {
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02T15:04:05.000-0700",
	})
	logrus.SetOutput(os.Stdout)
	logrus.SetLevel(logrus.DebugLevel)
}

func main() {
	// 配置日志

	logrus.Info("正在启动必应壁纸API服务...")

	// 加载配置
	if err := config.LoadConfig(); err != nil {
		logrus.Fatalf("加载配置失败: %v", err)
	}

	cfg := &config.AppConfig

	// 创建指标收集器
	metricsCollector := metrics.NewMetrics("bingwallpaper")

	// 初始化存储提供者
	var localStorageProvider storage.Provider
	var objectStorageProvider storage.Provider

	// 配置热重载回调
	configReloadCallback := func(newCfg *config.Config) {
		logrus.Info("检测到配置变更，正在重新加载服务...")
		initStorage(newCfg, &localStorageProvider, &objectStorageProvider)
		if bingService != nil {
			bingService.UpdateConfig(newCfg)
		}
		if scheduler != nil {
			scheduler.UpdateJobs(newCfg, bingService)
		}
	}

	// 启动配置监视
	config.WatchConfig(configReloadCallback)

	// 初始化存储
	initStorage(cfg, &localStorageProvider, &objectStorageProvider)

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
		logrus.Fatalf("创建元数据目录失败: %v", err)
	}

	// 初始化壁纸
	if err := initWallpaper(cfg); err != nil {
		logrus.Errorf("检查元数据失败: %v", err)
	}

	// 初始化定时任务
	scheduler = cron.NewScheduler()

	// 设置定时任务
	if err := scheduler.SetupJobs(cfg, bingService); err != nil {
		logrus.Fatalf("设置定时任务失败: %v", err)
	}

	// 启动定时任务
	scheduler.Start()
	defer scheduler.Stop()

	// 创建Gin路由
	router := exgin.Init(&exgin.Config{
		Debug:   true,
		Metrics: true,
	})

	// 添加中间件
	router.Use(exgin.ExLog("/metrics"), exgin.ExRecovery())
	router.Use(gzip.Gzip(gzip.DefaultCompression))

	// 创建API处理器
	handler := api.NewHandler(cfg, bingService)
	handler.SetupRoutes(router)
	addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)
	// 创建HTTP服务器
	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}
	go func() {
		exhttp.SetupGracefulStop(srv)
	}()
	logrus.Infof("http listen to %v, pid is %v", addr, os.Getpid())
	logrus.Infof("API基础路径: %s", cfg.API.BasePath)
	logrus.Infof("壁纸访问地址: http://localhost:%s/images", cfg.Server.Port)
	logrus.Infof("API端点: http://localhost:%s%s/wallpaper", cfg.Server.Port, cfg.API.BasePath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logrus.Errorf("Failed to start http server, error: %s", err)
	}
}

// initStorage 初始化存储提供者
func initStorage(cfg *config.Config, localStorageProvider, objectStorageProvider *storage.Provider) {
	// 初始化本地存储（始终启用）
	local, err := storage.NewLocalStorage(cfg.Storage.Local.Path)
	if err != nil {
		logrus.Fatalf("初始化本地存储失败: %v", err)
	}
	*localStorageProvider = local
	logrus.Infof("已初始化本地存储: %s", cfg.Storage.Local.Path)

	// 初始化对象存储
	if cfg.Storage.Object.Enabled {
		object, err := storage.NewObjectStorage(storage.ObjectStorageConfig{
			Endpoint:        cfg.Storage.Object.Endpoint,
			AccessKeyID:     cfg.Storage.Object.AccessKeyID,
			SecretAccessKey: cfg.Storage.Object.SecretAccessKey,
			BucketName:      cfg.Storage.Object.BucketName,
			Region:          cfg.Storage.Object.Region,
			UseSSL:          cfg.Storage.Object.UseSSL,
			BaseURL:         cfg.Storage.Object.BaseURL,
			PathPrefix:      cfg.Storage.Object.PathPrefix,
		})
		if err != nil {
			logrus.Fatalf("初始化对象存储失败: %v", err)
		}
		*objectStorageProvider = object
		logrus.Info("已初始化对象存储")
	} else {
		logrus.Info("对象存储已禁用")
	}
}

// initWallpaper 初始化壁纸
func initWallpaper(cfg *config.Config) error {
	// 检查元数据文件是否存在
	if _, err := os.Stat(cfg.Storage.MetadataPath); os.IsNotExist(err) {
		logrus.Info("未找到壁纸元数据，自动触发更新操作")

		// 检查本地存储目录是否存在
		localPath := cfg.Storage.Local.Path
		if _, err := os.Stat(localPath); os.IsNotExist(err) {
			if err := os.MkdirAll(localPath, 0755); err != nil {
				return fmt.Errorf("创建本地存储目录失败: %v", err)
			}
			logrus.Infof("已创建本地存储目录: %s", localPath)
		}

		// 自动触发更新操作
		updated, err := bingService.CheckAndUpdateWallpaper()
		if err != nil {
			return fmt.Errorf("自动更新壁纸失败: %v", err)
		}

		if updated {
			logrus.Info("壁纸已成功更新")
		} else {
			logrus.Warn("壁纸更新未成功，但未返回错误")
		}

		return nil
	}

	// 检查本地文件是否存在
	metadata, err := bingService.GetCurrentWallpaper()
	if err != nil {
		return fmt.Errorf("获取当前壁纸信息失败: %v", err)
	}

	localPath := filepath.Join(cfg.Storage.Local.Path, metadata.Storage.Filename)
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		logrus.Info("元数据存在但本地文件不存在，自动触发更新操作")

		// 自动触发更新操作
		updated, err := bingService.CheckAndUpdateWallpaper()
		if err != nil {
			return fmt.Errorf("自动更新壁纸失败: %v", err)
		}

		if updated {
			logrus.Info("壁纸已成功更新")
		} else {
			logrus.Warn("壁纸更新未成功，但未返回错误")
		}
	}

	return nil
}
