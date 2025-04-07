package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/sirupsen/logrus"
)

// 定义常见的对象存储错误
var (
	ErrObjectNotFound      = errors.New("对象不存在")
	ErrBucketNotFound      = errors.New("存储桶不存在")
	ErrObjectStorageAccess = errors.New("对象存储访问错误")
	ErrUploadFailed        = errors.New("对象上传失败")
)

// ObjectStorageConfig 对象存储配置
type ObjectStorageConfig struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	Region          string
	UseSSL          bool
	BaseURL         string // 可选，用于CDN
	PathPrefix      string // 路径前缀
	ChunkSize       int64  // 分片上传大小，默认5MB
	MaxRetries      int    // 最大重试次数
}

// ObjectStorage 实现S3兼容的对象存储
type ObjectStorage struct {
	useSSL     bool
	client     *minio.Client
	bucketName string
	region     string
	baseURL    string
	pathPrefix string
	chunkSize  int64
	maxRetries int
	mutex      sync.RWMutex // 用于保护客户端重新初始化
}

// NewObjectStorage 创建新的对象存储提供者
func NewObjectStorage(cfg ObjectStorageConfig) (*ObjectStorage, error) {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 5 * 1024 * 1024 // 默认5MB
	}

	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3 // 默认重试3次
	}

	// 创建MinIO客户端
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("创建对象存储客户端失败: %w", err)
	}

	if len(cfg.BaseURL) == 0 {
		cfg.BaseURL = cfg.Endpoint
	}

	storage := &ObjectStorage{
		client:     client,
		bucketName: cfg.BucketName,
		region:     cfg.Region,
		baseURL:    cfg.BaseURL,
		pathPrefix: cfg.PathPrefix,
		chunkSize:  cfg.ChunkSize,
		maxRetries: cfg.MaxRetries,
		useSSL:     cfg.UseSSL,
	}

	// 检查并初始化存储桶
	if err := storage.initBucket(context.Background()); err != nil {
		return nil, err
	}

	return storage, nil
}

// initBucket 初始化存储桶
func (s *ObjectStorage) initBucket(ctx context.Context) error {
	// 检查桶是否存在，不存在则创建
	exists, err := s.client.BucketExists(ctx, s.bucketName)
	if err != nil {
		return fmt.Errorf("检查桶是否存在失败: %w", err)
	}

	if !exists {
		logrus.Infof("桶 %s 不存在，正在创建...", s.bucketName)
		if err := s.client.MakeBucket(ctx, s.bucketName, minio.MakeBucketOptions{Region: s.region}); err != nil {
			return fmt.Errorf("创建桶失败: %w", err)
		}

		// 设置桶策略为公共读取
		policy := `{
			"Version": "2012-10-17",
			"Statement": [
				{
					"Effect": "Allow",
					"Principal": {"AWS": ["*"]},
					"Action": ["s3:GetObject"],
					"Resource": ["arn:aws:s3:::%s/*"]
				}
			]
		}`

		policy = fmt.Sprintf(policy, s.bucketName)
		if err := s.client.SetBucketPolicy(ctx, s.bucketName, policy); err != nil {
			logrus.Warnf("设置桶策略失败: %v", err)
		}
	}

	return nil
}

// Save 保存图片到对象存储
func (s *ObjectStorage) Save(ctx context.Context, data io.Reader, filename string, contentType string) (string, error) {
	// 处理路径前缀
	if s.pathPrefix != "" && !strings.HasPrefix(filename, s.pathPrefix) {
		filename = filepath.Join(s.pathPrefix, filename)
	}

	// 构建上传选项
	opts := minio.PutObjectOptions{
		ContentType: contentType,
	}

	// 重试逻辑
	var lastErr error
	var info minio.UploadInfo

	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		// 尝试上传
		uploadInfo, err := s.client.PutObject(ctx, s.bucketName, filename, data, -1, opts)
		if err == nil {
			info = uploadInfo
			break
		}

		lastErr = err
		logrus.Warnf("上传对象失败 (尝试 %d/%d): %v", attempt, s.maxRetries, err)

		// 如果是最后一次尝试，不需要等待
		if attempt < s.maxRetries {
			// 指数退避策略
			backoff := time.Duration(1<<uint(attempt-1)) * 100 * time.Millisecond
			time.Sleep(backoff)

			// 对于可寻址的Reader，尝试重置到开始位置
			if seeker, ok := data.(io.Seeker); ok {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					return "", fmt.Errorf("重置读取器位置失败: %w", err)
				}
			} else {
				// 如果不可寻址，则无法重试
				return "", fmt.Errorf("上传失败且无法重试: %w", lastErr)
			}
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("%w: %v", ErrUploadFailed, lastErr)
	}

	logrus.Infof("上传文件到对象存储: %s/%s (%d bytes)", s.bucketName, filename, info.Size)

	return s.GetURL(filename), nil
}

// SaveMultipart 使用分片上传保存大文件
func (s *ObjectStorage) SaveMultipart(ctx context.Context, data io.Reader, filename string, contentType string, size int64) (string, error) {
	// 处理路径前缀
	if s.pathPrefix != "" && !strings.HasPrefix(filename, s.pathPrefix) {
		filename = filepath.Join(s.pathPrefix, filename)
	}

	// 如果文件小于分片大小，使用普通上传
	if size > 0 && size < s.chunkSize {
		return s.Save(ctx, data, filename, contentType)
	}

	// 启动分片上传
	_, err := s.client.PutObject(ctx, s.bucketName, filename, data, -1, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("初始化分片上传失败: %w", err)
	}

	// 准备分片上传
	var parts []minio.CompletePart
	partNumber := 1

	for {
		// 读取一个分片大小的数据
		partBuffer := make([]byte, s.chunkSize)
		bytesRead, err := io.ReadFull(data, partBuffer)

		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			// 终止未完成的上传
			s.client.RemoveIncompleteUpload(ctx, s.bucketName, filename)
			return "", fmt.Errorf("读取上传数据失败: %w", err)
		}

		// 如果读取了一些数据
		if bytesRead > 0 {
			// 上传这一部分
			etag, err := s.client.PutObject(ctx, s.bucketName, fmt.Sprintf("%s.part%d", filename, partNumber),
				bytes.NewReader(partBuffer[:bytesRead]), int64(bytesRead), minio.PutObjectOptions{})

			if err != nil {
				// 终止未完成的上传
				s.client.RemoveIncompleteUpload(ctx, s.bucketName, filename)
				return "", fmt.Errorf("上传分片 %d 失败: %w", partNumber, err)
			}

			// 添加成功的分片
			parts = append(parts, minio.CompletePart{
				PartNumber: partNumber,
				ETag:       etag.ETag,
			})

			partNumber++
		}

		// 如果到达文件末尾，跳出循环
		if err == io.ErrUnexpectedEOF || err == io.EOF || bytesRead < int(s.chunkSize) {
			break
		}
	}

	// 完成分片上传
	_, err = s.client.ComposeObject(ctx, minio.CopyDestOptions{
		Bucket: s.bucketName,
		Object: filename,
	}, minio.CopySrcOptions{
		Bucket: s.bucketName,
		Object: fmt.Sprintf("%s.part%d", filename, 1),
	})
	if err != nil {
		// 尝试终止未完成的上传
		s.client.RemoveIncompleteUpload(ctx, s.bucketName, filename)
		return "", fmt.Errorf("完成分片上传失败: %w", err)
	}

	logrus.Infof("完成分片上传到对象存储: %s/%s (%d 个分片)", s.bucketName, filename, len(parts))

	return s.GetURL(filename), nil
}

// GetURL 获取对象的URL
func (s *ObjectStorage) GetURL(filename string) string {
	// 处理路径前缀
	if s.pathPrefix != "" && !strings.HasPrefix(filename, s.pathPrefix) {
		filename = filepath.Join(s.pathPrefix, filename)
	}

	// 否则使用默认S3 URL
	scheme := "http"
	if s.useSSL {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/%s/%s",
		scheme, s.baseURL, s.bucketName, filename)
}

// Delete 删除对象
func (s *ObjectStorage) Delete(ctx context.Context, filename string) error {
	// 处理路径前缀
	if s.pathPrefix != "" && !strings.HasPrefix(filename, s.pathPrefix) {
		filename = filepath.Join(s.pathPrefix, filename)
	}

	// 检查对象是否存在
	_, err := s.client.StatObject(ctx, s.bucketName, filename, minio.StatObjectOptions{})
	if err != nil {
		// 如果对象不存在，则忽略错误
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			logrus.Warnf("要删除的对象不存在: %s", filename)
			return nil
		}
		return fmt.Errorf("%w: %v", ErrObjectStorageAccess, err)
	}

	// 删除对象
	err = s.client.RemoveObject(ctx, s.bucketName, filename, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("删除对象失败: %w", err)
	}

	logrus.Infof("已删除对象: %s/%s", s.bucketName, filename)
	return nil
}

// ListObjects 列出指定前缀的所有对象
func (s *ObjectStorage) ListObjects(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	// 处理路径前缀
	if s.pathPrefix != "" && !strings.HasPrefix(prefix, s.pathPrefix) {
		prefix = filepath.Join(s.pathPrefix, prefix)
	}

	// 创建对象列表通道
	objectCh := s.client.ListObjects(ctx, s.bucketName, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: recursive,
	})

	var objects []string

	// 从通道获取对象列表
	for object := range objectCh {
		if object.Err != nil {
			return nil, fmt.Errorf("列出对象失败: %w", object.Err)
		}

		// 从完整路径中移除路径前缀（如果有的话）
		name := object.Key
		if s.pathPrefix != "" && strings.HasPrefix(name, s.pathPrefix) {
			name = strings.TrimPrefix(name, s.pathPrefix)
			name = strings.TrimPrefix(name, "/")
		}

		objects = append(objects, name)
	}

	return objects, nil
}

// Name 返回存储提供者名称
func (s *ObjectStorage) Name() string {
	return "object"
}

// PresignedURL 生成预签名URL用于临时访问
func (s *ObjectStorage) PresignedURL(ctx context.Context, filename string, expires time.Duration) (string, error) {
	// 处理路径前缀
	if s.pathPrefix != "" && !strings.HasPrefix(filename, s.pathPrefix) {
		filename = filepath.Join(s.pathPrefix, filename)
	}

	// 生成预签名URL
	presignedURL, err := s.client.PresignedGetObject(ctx, s.bucketName, filename, expires, nil)
	if err != nil {
		return "", fmt.Errorf("生成预签名URL失败: %w", err)
	}

	return presignedURL.String(), nil
}

// Reconnect 重新连接对象存储
func (s *ObjectStorage) Reconnect(cfg ObjectStorageConfig) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 创建新的MinIO客户端
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return fmt.Errorf("重新创建对象存储客户端失败: %w", err)
	}

	// 更新实例
	s.client = client
	s.bucketName = cfg.BucketName
	s.baseURL = cfg.BaseURL
	s.pathPrefix = cfg.PathPrefix

	// 验证连接和桶配置
	if err := s.initBucket(context.Background()); err != nil {
		return err
	}

	logrus.Info("对象存储客户端已重新连接")
	return nil
}
