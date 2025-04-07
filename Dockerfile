FROM ysicing/god AS builder

WORKDIR /app

ENV GOPROXY=https://goproxy.cn,direct

# 复制Go模块定义文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 编译应用
RUN CGO_ENABLED=0 GOOS=linux go build -o bingwallpaper cmd/server/main.go

# 使用轻量级基础镜像
FROM ysicing/debian

WORKDIR /app

# 设置时区
ENV TZ=Asia/Shanghai

# 创建缓存目录
RUN mkdir -p /app/cache/images /app/config

# 从构建阶段复制编译好的二进制文件
COPY --from=builder /app/bingwallpaper /app/

# 复制配置文件
COPY --from=builder /app/config/config.yaml /app/config/

# 设置权限
RUN chmod +x /app/bingwallpaper

# 设置环境变量
ENV BING_API_STORAGE_LOCAL_PATH=/app/cache/images
ENV BING_API_STORAGE_METADATAPATH=/app/cache/metadata.json

# 暴露端口
EXPOSE 3000

# 健康检查
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:3000/health || exit 1

# 运行应用
CMD ["/app/bingwallpaper"]
