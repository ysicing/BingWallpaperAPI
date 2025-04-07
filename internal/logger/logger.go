package logger

import (
	"os"
	"sync"
	
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// 全局记录器实例
	log *zap.Logger
	// 确保只初始化一次
	once sync.Once
)

// Init 初始化全局记录器
func Init() {
	once.Do(func() {
		// 配置编码器
		encoderConfig := zapcore.EncoderConfig{
			TimeKey:        "time",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.LowercaseLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		}
		
		// 输出到控制台
		consoleCore := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(os.Stdout),
			zap.NewAtomicLevelAt(zapcore.InfoLevel),
		)
		
		// 创建记录器
		log = zap.New(
			consoleCore,
			zap.AddCaller(),
			zap.AddStacktrace(zapcore.ErrorLevel),
		)
	})
}

// GetLogger 获取指定模块的记录器
func GetLogger(module string) *zap.SugaredLogger {
	if log == nil {
		Init()
	}
	return log.Named(module).Sugar()
}

// Sync 同步日志缓冲区
func Sync() {
	if log != nil {
		_ = log.Sync()
	}
}