# Bing Wallpaper API

必应每日壁纸API服务，提供壁纸下载、元数据获取等功能。

## 功能特点

- 自动获取必应每日壁纸
- 支持本地存储和对象存储
- 提供RESTful API接口
- 支持定时更新和清理
- 提供Prometheus指标监控

## 快速开始

### 使用Docker部署

1. 克隆仓库

```bash
git clone https://github.com/ysicing/BingWallpaperAPI.git
cd BingWallpaperAPI
```

2. 使用Docker Compose启动服务

```bash
docker-compose up -d
```

3. 访问API

- 获取壁纸信息：`http://localhost:3000/api/v1/wallpaper`
- 获取壁纸图片：`http://localhost:3000/images/[filename]`
- 获取元数据：`http://localhost:3000/metadata`
- 健康检查：`http://localhost:3000/health`

### 手动构建和运行

1. 构建应用

```bash
go build -o bin/server cmd/server/main.go
```

2. 运行应用

```bash
./bin/server
```

## 配置说明

配置文件位于`config/config.yaml`，主要配置项包括：

- 服务器配置：端口、主机等
- 存储配置：本地存储路径、对象存储参数等
- 定时任务配置：更新间隔、清理间隔等
- API配置：基础路径、速率限制等

## API文档

### 获取壁纸信息

```
GET /api/v1/wallpaper
```

返回当前壁纸的信息，包括图片URL和元数据。

### 获取壁纸图片

```
GET /images/[filename]
```

返回壁纸图片文件。

### 获取元数据

```
GET /metadata
```

返回壁纸的完整元数据。

### 手动更新壁纸

```
GET /api/v1/update
```

手动触发壁纸更新。

### 健康检查

```
GET /health
```

返回服务健康状态。

## 监控指标

服务提供了Prometheus格式的监控指标，可通过`/metrics`端点访问。

## 许可证

MIT 
