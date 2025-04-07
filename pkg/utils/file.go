package utils

import (
	"io"
	"os"
	"path/filepath"
)

// EnsureDirExists 确保目录存在，如果不存在则创建
func EnsureDirExists(dirPath string) error {
	return os.MkdirAll(dirPath, 0755)
}

// FileExists 检查文件是否存在
func FileExists(filePath string) bool {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false
	}
	return true
}

// SaveFile 将数据保存到文件中
func SaveFile(data []byte, filePath string) error {
	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := EnsureDirExists(dir); err != nil {
		return err
	}
	
	// 写入文件
	return os.WriteFile(filePath, data, 0644)
}

// SaveToFileFromReader 从读取器保存数据到文件
func SaveToFileFromReader(reader io.Reader, filePath string) error {
	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := EnsureDirExists(dir); err != nil {
		return err
	}
	
	// 创建文件
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	
	// 复制数据
	_, err = io.Copy(file, reader)
	return err
}

// ReadFile 读取文件内容
func ReadFile(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}