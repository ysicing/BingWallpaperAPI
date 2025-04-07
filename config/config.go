package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Config 应用程序配置
type Config struct {
	Server struct {
		Port string `mapstructure:"port"`
		Host string `mapstructure:"host"`
	} `mapstructure:"server"`

	Bing struct {
		ApiURL  string `mapstructure:"apiUrl"`
		BaseURL string `mapstructure:"baseUrl"`
	} `mapstructure:"bing"`

	Storage struct {
		// 本地存储配置
		Local struct {
			Path string `mapstructure:"path"`
		} `mapstructure:"local"`

		// 对象存储配置
		Object struct {
			Enabled         bool   `mapstructure:"enabled"`
			Endpoint        string `mapstructure:"endpoint"`
			AccessKeyID     string `mapstructure:"accessKeyID"`
			SecretAccessKey string `mapstructure:"secretAccessKey"`
			BucketName      string `mapstructure:"bucketName"`
			Region          string `mapstructure:"region"`
			UseSSL          bool   `mapstructure:"useSSL"`
			BaseURL         string `mapstructure:"baseURL"`
			PathPrefix      string `mapstructure:"pathPrefix"`
		} `mapstructure:"object"`

		// 元数据存储位置
		MetadataPath string `mapstructure:"metadataPath"`

		// 保留策略
		RetentionDays int `mapstructure:"retentionDays"`
	} `mapstructure:"storage"`

	Schedule struct {
		CheckInterval        string `mapstructure:"checkInterval"`
		CleanupInterval      string `mapstructure:"cleanupInterval"`
		LocalCleanupInterval string `mapstructure:"localCleanupInterval"`
	} `mapstructure:"schedule"`

	Retry struct {
		MaxAttempts int `mapstructure:"maxAttempts"`
		DelayMs     int `mapstructure:"delayMs"`
	} `mapstructure:"retry"`

	API struct {
		BasePath        string        `mapstructure:"basePath"`
		RateLimit       int           `mapstructure:"rateLimit"`
		RateLimitWindow time.Duration `mapstructure:"rateLimitWindow"`
	} `mapstructure:"api"`
}

var (
	// AppConfig 全局配置
	AppConfig Config
)

// LoadConfig 加载配置文件
func LoadConfig() error {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./config")

	// 设置环境变量前缀
	viper.SetEnvPrefix("BING_API")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// 设置默认值
	viper.SetDefault("server.port", "3000")
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("bing.apiUrl", "https://www.bing.com/HPImageArchive.aspx?format=js&idx=0&n=1")
	viper.SetDefault("bing.baseUrl", "https://www.bing.com")

	// 本地存储默认配置
	viper.SetDefault("storage.local.path", "./cache/images")

	// 对象存储默认配置
	viper.SetDefault("storage.object.enabled", false)
	viper.SetDefault("storage.object.endpoint", "s3.amazonaws.com")
	viper.SetDefault("storage.object.region", "us-east-1")
	viper.SetDefault("storage.object.useSSL", true)
	viper.SetDefault("storage.object.pathPrefix", "bing-wallpapers")

	// 元数据存储位置
	viper.SetDefault("storage.metadataPath", "./cache/metadata.json")
	viper.SetDefault("storage.retentionDays", 30)

	// 定时任务配置
	viper.SetDefault("schedule.checkInterval", "0 0 * * *")        // 每天午夜
	viper.SetDefault("schedule.cleanupInterval", "0 1 * * *")      // 每天凌晨1点
	viper.SetDefault("schedule.localCleanupInterval", "0 2 * * *") // 每天凌晨2点

	// 重试配置
	viper.SetDefault("retry.maxAttempts", 3)
	viper.SetDefault("retry.delayMs", 1000)

	// API配置
	viper.SetDefault("api.basePath", "/api/v1")
	viper.SetDefault("api.rateLimit", 60)
	viper.SetDefault("api.rateLimitWindow", "1m")

	// 尝试读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		// 如果配置文件不存在，创建默认配置文件
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			configDir := "./config"
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return err
			}

			// 创建默认配置文件
			if err := viper.SafeWriteConfigAs(filepath.Join(configDir, "config.yaml")); err != nil {
				return err
			}
			log.Println("已创建默认配置文件: config/config.yaml")
		} else {
			return err
		}
	}

	// 将配置映射到结构体
	if err := viper.Unmarshal(&AppConfig); err != nil {
		return err
	}

	// 验证必要的配置项
	if AppConfig.Storage.Object.Enabled {
		if AppConfig.Storage.Object.AccessKeyID == "" || AppConfig.Storage.Object.SecretAccessKey == "" {
			log.Println("警告: 对象存储已启用，但未提供访问凭证。这可能导致连接失败。")
		}

		if AppConfig.Storage.Object.BucketName == "" {
			return fmt.Errorf("对象存储已启用，但未提供桶名称")
		}
	}

	return nil
}

// GetStorageFilename 根据日期和路径前缀生成存储文件名
func GetStorageFilename(date time.Time, prefix string) string {
	year, month, day := date.Date()
	return filepath.Join(prefix, fmt.Sprintf("%d/%02d/%02d/wallpaper.jpg", year, month, day))
}

// GetMetadataFilename 根据日期和路径前缀生成元数据文件名
func GetMetadataFilename(date time.Time, prefix string) string {
	year, month, day := date.Date()
	return filepath.Join(prefix, fmt.Sprintf("%d/%02d/%02d/metadata.json", year, month, day))
}

// WatchConfig 监视配置文件变更
func WatchConfig(callback func(*Config)) {
	viper.OnConfigChange(func(e fsnotify.Event) {
		logrus.Infof("配置文件已修改: %s", e.Name)

		// 重新加载配置
		if err := viper.ReadInConfig(); err != nil {
			logrus.Errorf("重新加载配置失败: %v", err)
			return
		}

		// 解析配置到结构体
		oldConfig := AppConfig
		newConfig := Config{}
		if err := viper.Unmarshal(&newConfig); err != nil {
			logrus.Errorf("解析配置失败: %v", err)
			return
		}

		// 验证新配置
		if newConfig.Storage.Object.Enabled {
			if newConfig.Storage.Object.AccessKeyID == "" || newConfig.Storage.Object.SecretAccessKey == "" {
				logrus.Warn("警告: 对象存储已启用，但未提供访问凭证。这可能导致连接失败。")
			}

			if newConfig.Storage.Object.BucketName == "" {
				logrus.Error("对象存储已启用，但未提供桶名称，忽略此次配置更新")
				return
			}
		}

		// 更新全局配置
		AppConfig = newConfig

		// 调用回调函数
		if callback != nil {
			callback(&newConfig)
		}

		// 记录更改
		logConfigChanges(&oldConfig, &newConfig)
	})

	// 开始监视配置文件
	viper.WatchConfig()
	logrus.Info("已启动配置文件监视")
}

// logConfigChanges 记录配置更改
func logConfigChanges(old, new *Config) {
	// 记录主要配置变更
	if old.Server.Port != new.Server.Port || old.Server.Host != new.Server.Host {
		logrus.Infof("服务器配置已更改: %s:%s -> %s:%s",
			old.Server.Host, old.Server.Port, new.Server.Host, new.Server.Port)
	}

	// 记录定时任务配置变更
	if old.Schedule.CheckInterval != new.Schedule.CheckInterval {
		logrus.Infof("壁纸检查间隔已更改: %s -> %s", old.Schedule.CheckInterval, new.Schedule.CheckInterval)
	}

	if old.Schedule.CleanupInterval != new.Schedule.CleanupInterval {
		logrus.Infof("清理间隔已更改: %s -> %s", old.Schedule.CleanupInterval, new.Schedule.CleanupInterval)
	}

	if old.Schedule.LocalCleanupInterval != new.Schedule.LocalCleanupInterval {
		logrus.Infof("本地清理间隔已更改: %s -> %s", old.Schedule.LocalCleanupInterval, new.Schedule.LocalCleanupInterval)
	}

	// 记录保留策略变更
	if old.Storage.RetentionDays != new.Storage.RetentionDays {
		logrus.Infof("文件保留天数已更改: %d -> %d", old.Storage.RetentionDays, new.Storage.RetentionDays)
	}
}

// ReloadConfig 手动重新加载配置
func ReloadConfig() error {
	// 重新加载配置
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("重新加载配置失败: %w", err)
	}

	// 解析配置到结构体
	if err := viper.Unmarshal(&AppConfig); err != nil {
		return fmt.Errorf("解析配置失败: %w", err)
	}

	return nil
}
