package bing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ysicing/BingWallpaperAPI/config"
	"github.com/ysicing/BingWallpaperAPI/internal/logger"
	"github.com/ysicing/BingWallpaperAPI/pkg/metrics"
	"github.com/ysicing/BingWallpaperAPI/pkg/storage"
	"go.uber.org/zap"
)

// 定义服务错误
var (
	ErrBingAPIFailed    = errors.New("必应API请求失败")
	ErrNoWallpaperFound = errors.New("未找到壁纸")
	ErrStorageFailed    = errors.New("存储操作失败")
)

// BingImageResponse 必应API返回的数据结构
type BingImageResponse struct {
	Images []struct {
		URL           string `json:"url"`
		URLBase       string `json:"urlbase"`
		Copyright     string `json:"copyright"`
		Title         string `json:"title"`
		StartDate     string `json:"startdate"`
		FullStartDate string `json:"fullstartdate"`
		EndDate       string `json:"enddate"`
	} `json:"images"`
}

// EnhancedMetadata 增强的元数据结构，包含存储URL信息
type EnhancedMetadata struct {
	BingResponse BingImageResponse `json:"bing_response"`
	Storage      struct {
		LocalURL    string    `json:"local_url,omitempty"`
		ObjectURL   string    `json:"object_url,omitempty"`
		OriginalURL string    `json:"original_url,omitempty"`
		ContentType string    `json:"content_type"`
		Size        int64     `json:"size"`
		LastUpdated time.Time `json:"last_updated"`
		Filename    string    `json:"filename"`
	} `json:"storage"`
}

// Service 必应壁纸服务
type Service struct {
	log           *zap.SugaredLogger
	httpClient    *http.Client
	cfg           *config.Config
	localStorage  storage.Provider
	objectStorage storage.Provider
	mutex         sync.RWMutex // 保护更新操作
	metrics       *metrics.Metrics
}

// ServiceOptions 服务选项
type ServiceOptions struct {
	Config        *config.Config
	LocalStorage  storage.Provider
	ObjectStorage storage.Provider
	Metrics       *metrics.Metrics
	HTTPTimeout   time.Duration
}

// NewService 创建新的必应服务实例
func NewService(opts ServiceOptions) *Service {
	// 如果未提供超时时间，设置默认值
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = 30 * time.Second
	}

	return &Service{
		log:           logger.GetLogger("bing-service"),
		httpClient:    &http.Client{Timeout: opts.HTTPTimeout},
		cfg:           opts.Config,
		localStorage:  opts.LocalStorage,
		objectStorage: opts.ObjectStorage,
		metrics:       opts.Metrics,
	}
}

// CheckAndUpdateWallpaper 检查并更新壁纸
func (s *Service) CheckAndUpdateWallpaper() (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 记录开始时间（用于性能指标）
	startTime := time.Now()
	if s.metrics != nil {
		defer func() {
			s.metrics.UpdateDuration.Observe(time.Since(startTime).Seconds())
		}()
	}

	// 获取必应壁纸元数据
	s.log.Info("获取必应壁纸元数据...")
	bingData, err := s.fetchBingData(ctx)
	if err != nil {
		if s.metrics != nil {
			s.metrics.Errors.WithLabelValues("fetch_bing_data").Inc()
		}
		return false, fmt.Errorf("%w: %v", ErrBingAPIFailed, err)
	}

	if len(bingData.Images) == 0 {
		if s.metrics != nil {
			s.metrics.Errors.WithLabelValues("no_images").Inc()
		}
		return false, ErrNoWallpaperFound
	}

	// 提取第一张图片信息
	imageInfo := bingData.Images[0]

	// 构建完整的图片URL
	imageURL := s.cfg.Bing.BaseURL + imageInfo.URL
	s.log.Infof("找到壁纸: %s", imageURL)

	// 检查是否需要更新
	needsUpdate, _, err := s.needsUpdate(bingData)
	if err != nil {
		s.log.Warnf("检查更新状态时出错: %v", err)
	}

	if !needsUpdate {
		s.log.Info("壁纸不需要更新")
		if s.metrics != nil {
			s.metrics.UpdateSkipped.Inc()
		}
		return false, nil
	}

	// 下载图片
	s.log.Infof("开始下载壁纸: %s", imageURL)
	imageData, contentType, size, err := s.downloadImage(ctx, imageURL)
	if err != nil {
		if s.metrics != nil {
			s.metrics.Errors.WithLabelValues("download_image").Inc()
		}
		return false, fmt.Errorf("下载图片失败: %w", err)
	}

	// 记录已下载的图片大小
	if s.metrics != nil {
		s.metrics.ImageSize.Observe(float64(size))
	}

	// 今天的日期
	today := time.Now()

	// 准备增强的元数据
	enhancedMetadata := EnhancedMetadata{
		BingResponse: *bingData,
	}
	enhancedMetadata.Storage.ContentType = contentType
	enhancedMetadata.Storage.Size = size
	enhancedMetadata.Storage.LastUpdated = today
	enhancedMetadata.Storage.OriginalURL = imageURL

	// 构建文件名
	pathPrefix := s.cfg.Storage.Object.PathPrefix
	filename := config.GetStorageFilename(today, pathPrefix)
	enhancedMetadata.Storage.Filename = filename

	// 使用WaitGroup和通道来处理并发存储
	var wg sync.WaitGroup
	errorCh := make(chan error, 2)

	// 保存到本地存储（如果启用）
	if s.localStorage != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()

			localStartTime := time.Now()
			localURL, err := s.localStorage.Save(ctx, bytes.NewReader(imageData), filename, contentType)
			if err != nil {
				errorCh <- fmt.Errorf("保存到本地存储失败: %w", err)
				if s.metrics != nil {
					s.metrics.Errors.WithLabelValues("local_storage").Inc()
				}
				return
			}

			enhancedMetadata.Storage.LocalURL = localURL
			s.log.Infof("图片已保存到本地存储: %s (用时: %v)", localURL, time.Since(localStartTime))

			if s.metrics != nil {
				s.metrics.StorageDuration.WithLabelValues("local").Observe(time.Since(localStartTime).Seconds())
			}
		}()
	}

	// 保存到对象存储（如果启用）
	if s.objectStorage != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()

			objectStartTime := time.Now()

			// 检查是否支持分片上传
			var objectURL string
			var err error

			if multipartUploader, ok := s.objectStorage.(interface {
				SaveMultipart(context.Context, io.Reader, string, string, int64) (string, error)
			}); ok && size > 5*1024*1024 { // 大于5MB使用分片上传
				objectURL, err = multipartUploader.SaveMultipart(ctx, bytes.NewReader(imageData), filename, contentType, size)
			} else {
				objectURL, err = s.objectStorage.Save(ctx, bytes.NewReader(imageData), filename, contentType)
			}

			if err != nil {
				errorCh <- fmt.Errorf("保存到对象存储失败: %w", err)
				if s.metrics != nil {
					s.metrics.Errors.WithLabelValues("object_storage").Inc()
				}
				return
			}

			enhancedMetadata.Storage.ObjectURL = objectURL
			s.log.Infof("图片已保存到对象存储: %s (用时: %v)", objectURL, time.Since(objectStartTime))

			if s.metrics != nil {
				s.metrics.StorageDuration.WithLabelValues("object").Observe(time.Since(objectStartTime).Seconds())
			}
		}()
	}

	// 等待所有存储操作完成
	wg.Wait()
	close(errorCh)

	// 检查是否有错误
	var errs []error
	for err := range errorCh {
		errs = append(errs, err)
	}

	// 如果所有存储都失败，则返回错误
	if len(errs) > 0 && ((s.localStorage != nil && enhancedMetadata.Storage.LocalURL == "") &&
		(s.objectStorage != nil && enhancedMetadata.Storage.ObjectURL == "")) {
		return false, fmt.Errorf("%w: %v", ErrStorageFailed, errs[0])
	}

	// 并发保存元数据
	metadataWg := sync.WaitGroup{}
	metadataErrs := make(chan error, 2)

	// 保存增强的元数据
	metadataFilename := config.GetMetadataFilename(today, pathPrefix)
	metadataWg.Add(1)
	go func() {
		defer metadataWg.Done()
		if err := s.saveEnhancedMetadata(ctx, enhancedMetadata, metadataFilename); err != nil {
			metadataErrs <- fmt.Errorf("保存增强元数据失败: %w", err)
			s.log.Errorf("保存增强元数据失败: %v", err)
			if s.metrics != nil {
				s.metrics.Errors.WithLabelValues("save_metadata").Inc()
			}
		}
	}()

	// 保存当前元数据
	metadataWg.Add(1)
	go func() {
		defer metadataWg.Done()
		if err := s.saveCurrentMetadata(enhancedMetadata); err != nil {
			metadataErrs <- fmt.Errorf("保存当前元数据失败: %w", err)
			s.log.Errorf("保存当前元数据失败: %v", err)
			if s.metrics != nil {
				s.metrics.Errors.WithLabelValues("save_current_metadata").Inc()
			}
		}
	}()

	// 等待元数据保存完成
	metadataWg.Wait()
	close(metadataErrs)

	// 检查元数据保存错误
	for err := range metadataErrs {
		errs = append(errs, err)
	}

	// 即使有元数据保存错误，我们也视为更新成功，因为图片已经保存
	s.log.Info("壁纸已成功更新")

	if s.metrics != nil {
		s.metrics.UpdateSuccess.Inc()
	}

	return true, nil
}

// UpdateConfig 更新服务配置
func (s *Service) UpdateConfig(cfg *config.Config) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.log.Info("更新服务配置")
	s.cfg = cfg

	// 根据配置更新HTTP客户端超时
	s.httpClient.Timeout = 30 * time.Second
}

// Reload 重新初始化服务
func (s *Service) Reload(cfg *config.Config, localStorage, objectStorage storage.Provider) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.cfg = cfg
	s.localStorage = localStorage
	s.objectStorage = objectStorage

	s.log.Info("服务已重新初始化")
}

// fetchBingData 从必应API获取数据
func (s *Service) fetchBingData(ctx context.Context) (*BingImageResponse, error) {
	cfg := s.cfg
	var response *http.Response
	var err error

	// 重试机制
	for attempt := 1; attempt <= cfg.Retry.MaxAttempts; attempt++ {
		response, err = s.httpClient.Get(cfg.Bing.ApiURL)
		if err == nil && response.StatusCode == http.StatusOK {
			break
		}

		if response != nil {
			response.Body.Close()
		}

		if attempt < cfg.Retry.MaxAttempts {
			s.log.Warnf("API请求失败（尝试 %d/%d），等待重试: %v",
				attempt, cfg.Retry.MaxAttempts, err)
			time.Sleep(time.Duration(cfg.Retry.DelayMs) * time.Millisecond)
		}
	}

	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	// 读取并解析响应
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	var bingData BingImageResponse
	if err := json.Unmarshal(body, &bingData); err != nil {
		return nil, err
	}

	return &bingData, nil
}

// needsUpdate 检查壁纸是否需要更新
func (s *Service) needsUpdate(newData *BingImageResponse) (bool, *EnhancedMetadata, error) {
	cfg := s.cfg

	// 检查元数据文件是否存在
	currentMetadataPath := cfg.Storage.MetadataPath

	// 读取现有元数据
	file, err := os.Open(currentMetadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil, nil
		}
		return true, nil, err
	}
	defer file.Close()

	var existingData EnhancedMetadata
	if err := json.NewDecoder(file).Decode(&existingData); err != nil {
		return true, nil, err
	}

	// 比较日期
	if len(existingData.BingResponse.Images) > 0 && len(newData.Images) > 0 {
		existingDate := existingData.BingResponse.Images[0].StartDate
		newDate := newData.Images[0].StartDate

		if existingDate == newDate {
			s.log.Infof("壁纸已是最新（日期: %s）", newDate)
			return false, &existingData, nil
		}
	}

	return true, &existingData, nil
}

// downloadImage 下载图片
func (s *Service) downloadImage(ctx context.Context, imageURL string) ([]byte, string, int64, error) {
	cfg := s.cfg
	var response *http.Response
	var err error

	// 重试机制
	for attempt := 1; attempt <= cfg.Retry.MaxAttempts; attempt++ {
		response, err = s.httpClient.Get(imageURL)
		if err == nil && response.StatusCode == http.StatusOK {
			break
		}

		if response != nil {
			response.Body.Close()
		}

		if attempt < cfg.Retry.MaxAttempts {
			s.log.Warnf("图片下载失败（尝试 %d/%d），等待重试: %v",
				attempt, cfg.Retry.MaxAttempts, err)
			time.Sleep(time.Duration(cfg.Retry.DelayMs) * time.Millisecond)
		}
	}

	if err != nil {
		return nil, "", 0, fmt.Errorf("下载图片失败: %w", err)
	}
	defer response.Body.Close()

	// 检查状态码
	if response.StatusCode != http.StatusOK {
		return nil, "", 0, fmt.Errorf("下载图片失败，HTTP状态码: %d", response.StatusCode)
	}

	// 读取图片数据
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "", 0, fmt.Errorf("读取图片数据失败: %w", err)
	}

	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg" // 默认MIME类型
	}

	s.log.Infof("已下载图片: %d bytes, content-type: %s", len(data), contentType)
	return data, contentType, int64(len(data)), nil
}

// saveEnhancedMetadata 保存增强的元数据到特定位置
func (s *Service) saveEnhancedMetadata(ctx context.Context, metadata EnhancedMetadata, filename string) error {
	// 将元数据转换为JSON
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	// 保存到本地存储
	if s.localStorage != nil {
		_, err := s.localStorage.Save(ctx, bytes.NewReader(metadataBytes), filename, "application/json")
		if err != nil {
			return err
		}
	}

	// 保存到对象存储
	if s.objectStorage != nil {
		_, err := s.objectStorage.Save(ctx, bytes.NewReader(metadataBytes), filename, "application/json")
		if err != nil {
			return err
		}
	}

	return nil
}

// saveCurrentMetadata 保存当前元数据（最新的）
func (s *Service) saveCurrentMetadata(metadata EnhancedMetadata) error {
	// 将元数据转换为JSON
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	// 确保目录存在
	metadataPath := s.cfg.Storage.MetadataPath
	dir := filepath.Dir(metadataPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// 保存到文件
	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		return err
	}

	s.log.Infof("已保存最新元数据到: %s", metadataPath)
	return nil
}

// GetCurrentWallpaper 获取当前壁纸信息
func (s *Service) GetCurrentWallpaper() (*EnhancedMetadata, error) {
	metadataPath := s.cfg.Storage.MetadataPath

	// 读取元数据文件
	file, err := os.Open(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("未找到壁纸元数据")
		}
		return nil, err
	}
	defer file.Close()

	var metadata EnhancedMetadata
	if err := json.NewDecoder(file).Decode(&metadata); err != nil {
		return nil, err
	}

	return &metadata, nil
}
