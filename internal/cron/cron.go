package cron

import (
	"github.com/robfig/cron/v3"
	"github.com/sirupsen/logrus"
	"github.com/ysicing/BingWallpaperAPI/config"
	"github.com/ysicing/BingWallpaperAPI/internal/bing"
)

// Scheduler 定时任务调度器
type Scheduler struct {
	cron *cron.Cron
}

// NewScheduler 创建新的定时任务调度器
func NewScheduler() *Scheduler {
	return &Scheduler{
		cron: cron.New(),
	}
}

// Start 启动定时任务
func (s *Scheduler) Start() {
	s.cron.Start()
	logrus.Info("已启动定时任务")
}

// Stop 停止定时任务
func (s *Scheduler) Stop() {
	s.cron.Stop()
	logrus.Info("已停止定时任务")
}

// SetupJobs 设置定时任务
func (s *Scheduler) SetupJobs(cfg *config.Config, bingService *bing.Service) error {
	// 添加壁纸更新任务
	_, err := s.cron.AddFunc(cfg.Schedule.CheckInterval, func() {
		logrus.Info("执行定时壁纸更新")
		if _, err := bingService.CheckAndUpdateWallpaper(); err != nil {
			logrus.Errorf("定时壁纸更新失败: %v", err)
		}
	})

	if err != nil {
		return err
	}

	// 添加清理任务
	_, err = s.cron.AddFunc(cfg.Schedule.CleanupInterval, func() {
		logrus.Info("执行定时清理")
		if err := bingService.CleanupOldFiles(); err != nil {
			logrus.Errorf("定时清理失败: %v", err)
		}
	})

	if err != nil {
		return err
	}

	// 添加本地图片清理任务
	_, err = s.cron.AddFunc(cfg.Schedule.LocalCleanupInterval, func() {
		logrus.Info("执行本地图片清理")
		if err := bingService.CleanupOldLocalImages(); err != nil {
			logrus.Errorf("本地图片清理失败: %v", err)
		}
	})

	return err
}

// UpdateJobs 更新定时任务
func (s *Scheduler) UpdateJobs(cfg *config.Config, bingService *bing.Service) {
	// 停止当前任务
	s.cron.Stop()

	// 清空所有任务
	s.cron = cron.New()

	// 重新设置任务
	if err := s.SetupJobs(cfg, bingService); err != nil {
		logrus.Errorf("更新定时任务失败: %v", err)
	}

	// 重新启动
	s.cron.Start()
	logrus.Info("已更新并重启定时任务")
}
