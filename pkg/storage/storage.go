package storage

import (
	"context"
	"io"
	"time"
)

// Provider 定义存储提供者接口
type Provider interface {
	// Save 保存图片到存储，返回公开访问URL
	Save(ctx context.Context, data io.Reader, filename string, contentType string) (string, error)
	
	// GetURL 获取指定文件的URL
	GetURL(filename string) string
	
	// Delete 删除文件（用于清理过期文件）
	Delete(ctx context.Context, filename string) error
	
	// Name 返回存储提供者名称
	Name() string
}

// ImageInfo 包含图片信息和多种URL
type ImageInfo struct {
	Filename      string    `json:"filename"`
	LocalURL      string    `json:"local_url,omitempty"`
	ObjectURL     string    `json:"object_url,omitempty"`
	OriginalURL   string    `json:"original_url,omitempty"`
	ContentType   string    `json:"content_type,omitempty"`
	Size          int64     `json:"size,omitempty"`
	LastUpdated   time.Time `json:"last_updated"`
}