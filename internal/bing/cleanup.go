package bing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// CleanupOldFiles 清理过期的文件
func (s *Service) CleanupOldFiles() error {
	cfg := s.cfg
	retentionDays := cfg.Storage.RetentionDays
	if retentionDays <= 0 {
		logrus.Info("文件保留天数设置为0或负数，跳过清理")
		return nil
	}

	// 计算截止日期
	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)
	logrus.Infof("清理早于 %s 的文件", cutoffDate.Format("2006-01-02"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	errorCh := make(chan error, 2) // 用于收集错误

	// 并发清理本地存储和对象存储
	if s.localStorage != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.cleanupLocalStorage(ctx, cutoffDate); err != nil {
				errorCh <- fmt.Errorf("清理本地存储失败: %w", err)
			}
		}()
	}

	if s.objectStorage != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.cleanupObjectStorage(ctx, cutoffDate); err != nil {
				errorCh <- fmt.Errorf("清理对象存储失败: %w", err)
			}
		}()
	}

	// 等待所有清理任务完成
	wg.Wait()
	close(errorCh)

	// 处理可能的错误
	var errs []string
	for err := range errorCh {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("清理过程中发生错误: %s", strings.Join(errs, "; "))
	}

	logrus.Info("清理过期文件完成")
	return nil
}

// cleanupLocalStorage 清理本地存储中的过期文件
func (s *Service) cleanupLocalStorage(ctx context.Context, cutoffDate time.Time) error {
	// 获取本地存储路径
	localPath := s.cfg.Storage.Local.Path

	// 确保路径存在
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		return nil // 目录不存在，无需清理
	}

	logrus.Infof("开始清理本地存储目录: %s", localPath)
	deletedCount := 0

	// 遍历本地存储目录
	err := filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			logrus.Warnf("访问路径 %s 时出错: %v", path, err)
			return nil // 继续遍历
		}

		// 跳过目录
		if info.IsDir() {
			return nil
		}

		// 解析路径中的日期
		// 假设路径格式为 basePath/year/month/day/filename
		pathComponents := strings.Split(path, string(os.PathSeparator))
		if len(pathComponents) < 4 { // 不符合预期的路径格式
			return nil
		}

		// 从路径中提取年、月、日
		var year, month, day int
		_, err = fmt.Sscanf(strings.Join(pathComponents[len(pathComponents)-4:len(pathComponents)-1], "/"), "%d/%02d/%02d", &year, &month, &day)
		if err != nil {
			// 不是按日期组织的文件，跳过
			return nil
		}

		// 构建文件日期
		fileDate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)

		// 如果文件日期在截止日期之前，删除文件
		if fileDate.Before(cutoffDate) {
			if err := os.Remove(path); err != nil {
				logrus.Warnf("删除文件 %s 失败: %v", path, err)
				return nil // 继续处理其他文件
			}

			deletedCount++
			logrus.Debugf("已删除过期文件: %s", path)
		}

		return nil
	})

	if err != nil {
		return err
	}

	// 删除空目录
	if err := s.removeEmptyDirs(localPath); err != nil {
		logrus.Warnf("清理空目录失败: %v", err)
	}

	logrus.Infof("本地存储清理完成，共删除 %d 个过期文件", deletedCount)
	return nil
}

// removeEmptyDirs 递归删除空目录
func (s *Service) removeEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			path := filepath.Join(dir, entry.Name())
			if err := s.removeEmptyDirs(path); err != nil {
				return err
			}
		}
	}

	// 重新读取目录，检查是否为空
	entries, err = os.ReadDir(dir)
	if err != nil {
		return err
	}

	// 如果目录为空且不是根目录，则删除
	if len(entries) == 0 && dir != s.cfg.Storage.Local.Path {
		if err := os.Remove(dir); err != nil {
			return err
		}
		logrus.Debugf("已删除空目录: %s", dir)
	}

	return nil
}

// cleanupObjectStorage 清理对象存储中的过期文件
func (s *Service) cleanupObjectStorage(ctx context.Context, cutoffDate time.Time) error {
	if s.objectStorage == nil {
		return nil // 没有配置对象存储
	}

	logrus.Info("开始清理对象存储中的过期文件")

	// 获取对象存储中的前缀（基础路径）
	prefix := s.cfg.Storage.Object.PathPrefix

	// 假设为了使用日期组织文件，年份目录作为第一级
	// 列出所有年份目录
	years, err := s.objectStorage.(interface {
		ListObjects(context.Context, string, bool) ([]string, error)
	}).ListObjects(ctx, prefix, false)
	if err != nil {
		return fmt.Errorf("列出年份目录失败: %w", err)
	}

	deletedCount := 0

	// 遍历年份目录
	for _, yearPath := range years {
		// 提取年份
		var year int
		_, err := fmt.Sscanf(filepath.Base(yearPath), "%d", &year)
		if err != nil {
			continue // 不是年份目录，跳过
		}

		// 如果整个年份都在截止日期之前，可以批量删除
		if year < cutoffDate.Year() {
			// 列出该年份下的所有文件
			objects, err := s.objectStorage.(interface {
				ListObjects(context.Context, string, bool) ([]string, error)
			}).ListObjects(ctx, yearPath, true)
			if err != nil {
				logrus.Warnf("列出年份 %d 的文件失败: %v", year, err)
				continue
			}

			// 删除所有对象
			for _, obj := range objects {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				if err := s.objectStorage.Delete(ctx, obj); err != nil {
					logrus.Warnf("删除对象 %s 失败: %v", obj, err)
					continue
				}

				deletedCount++
			}

			continue
		}

		// 如果年份是当前年份或上一年，需要按月和日检查
		// 列出该年份下的所有月份
		monthPaths, err := s.objectStorage.(interface {
			ListObjects(context.Context, string, bool) ([]string, error)
		}).ListObjects(ctx, yearPath, false)
		if err != nil {
			logrus.Warnf("列出年份 %d 的月份失败: %v", year, err)
			continue
		}

		for _, monthPath := range monthPaths {
			// 提取月份
			var month int
			_, err := fmt.Sscanf(filepath.Base(monthPath), "%02d", &month)
			if err != nil {
				continue // 不是月份目录，跳过
			}

			// 检查年月是否在截止日期之前
			if year < cutoffDate.Year() || (year == cutoffDate.Year() && month < int(cutoffDate.Month())) {
				// 列出该月份下的所有文件
				objects, err := s.objectStorage.(interface {
					ListObjects(context.Context, string, bool) ([]string, error)
				}).ListObjects(ctx, monthPath, true)
				if err != nil {
					logrus.Warnf("列出年份 %d 月份 %d 的文件失败: %v", year, month, err)
					continue
				}

				// 删除所有对象
				for _, obj := range objects {
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
					}

					if err := s.objectStorage.Delete(ctx, obj); err != nil {
						logrus.Warnf("删除对象 %s 失败: %v", obj, err)
						continue
					}

					deletedCount++
				}

				continue
			}

			// 如果是当前年月，则需要按日检查
			if year == cutoffDate.Year() && month == int(cutoffDate.Month()) {
				// 列出该月份下的所有日
				dayPaths, err := s.objectStorage.(interface {
					ListObjects(context.Context, string, bool) ([]string, error)
				}).ListObjects(ctx, monthPath, false)
				if err != nil {
					logrus.Warnf("列出年份 %d 月份 %d 的日期失败: %v", year, month, err)
					continue
				}

				for _, dayPath := range dayPaths {
					// 提取日
					var day int
					_, err := fmt.Sscanf(filepath.Base(dayPath), "%02d", &day)
					if err != nil {
						continue // 不是日期目录，跳过
					}

					// 检查年月日是否在截止日期之前
					fileDate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
					if fileDate.Before(cutoffDate) {
						// 列出该日下的所有文件
						objects, err := s.objectStorage.(interface {
							ListObjects(context.Context, string, bool) ([]string, error)
						}).ListObjects(ctx, dayPath, true)
						if err != nil {
							logrus.Warnf("列出年份 %d 月份 %d 日期 %d 的文件失败: %v", year, month, day, err)
							continue
						}

						// 删除所有对象
						for _, obj := range objects {
							select {
							case <-ctx.Done():
								return ctx.Err()
							default:
							}

							if err := s.objectStorage.Delete(ctx, obj); err != nil {
								logrus.Warnf("删除对象 %s 失败: %v", obj, err)
								continue
							}

							deletedCount++
						}
					}
				}
			}
		}
	}

	logrus.Infof("对象存储清理完成，共删除 %d 个过期文件", deletedCount)
	return nil
}
