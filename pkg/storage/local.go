package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	
	"go.uber.org/zap"
)

// LocalStorage 实现本地文件系统存储
type LocalStorage struct {
	basePath   string
	baseURL    string
	logger     *zap.SugaredLogger
}

// NewLocalStorage 创建新的本地存储提供者
func NewLocalStorage(basePath, baseURL string, logger *zap.SugaredLogger) (*LocalStorage, error) {
	// 确保存储目录存在
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("创建存储目录失败: %w", err)
	}
	
	return &LocalStorage{
		basePath: basePath,
		baseURL:  baseURL,
		logger:   logger,
	}, nil
}

// Save 保存图片到本地文件系统
func (s *LocalStorage) Save(ctx context.Context, data io.Reader, filename string, contentType string) (string, error) {
	fullPath := filepath.Join(s.basePath, filename)
	dirPath := filepath.Dir(fullPath)
	
	// 确保目录存在
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	
	// 创建文件
	file, err := os.Create(fullPath)
	if err != nil {
		return "", fmt.Errorf("创建文件失败: %w", err)
	}
	defer file.Close()
	
	// 复制数据
	size, err := io.Copy(file, data)
	if err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}
	
	s.logger.Infof("保存文件到本地: %s (%d bytes)", fullPath, size)
	
	// 返回文件的URL
	return s.GetURL(filename), nil
}

// GetURL 获取文件的URL
func (s *LocalStorage) GetURL(filename string) string {
	// 构建本地URL
	return fmt.Sprintf("%s/%s", s.baseURL, filename)
}

// Delete 删除本地文件
func (s *LocalStorage) Delete(ctx context.Context, filename string) error {
	fullPath := filepath.Join(s.basePath, filename)
	
	if err := os.Remove(fullPath); err != nil {
		if os.IsNotExist(err) {
			s.logger.Warnf("要删除的文件不存在: %s", fullPath)
			return nil
		}
		return fmt.Errorf("删除文件失败: %w", err)
	}
	
	s.logger.Infof("已删除本地文件: %s", fullPath)
	return nil
}

// Name 返回存储提供者名称
func (s *LocalStorage) Name() string {
	return "local"
}