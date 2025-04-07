FROM golang:1.23-alpine AS builder

WORKDIR /app

# 复制Go模块定义文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 编译应用
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o bingwallpaper ./cmd/server

# 使用轻量级基础镜像
FROM alpine:3.19

WORKDIR /app

# 安装CA证书以支持HTTPS请求
RUN apk --no-cache add ca-certificates tzdata

# 设置时区
ENV TZ=Asia/Shanghai

# 创建缓存目录
RUN mkdir -p /app/cache/images

# 从构建阶段复制编译好的二进制文件
COPY --from=builder /app/bingwallpaper /app/

# 复制配置文件
COPY --from=builder /app/config/config.yaml /app/config/

# 设置权限
RUN chmod +x /app/bingwallpaper

# 暴露端口
EXPOSE 3000

# 运行应用
CMD ["/app/bingwallpaper"]