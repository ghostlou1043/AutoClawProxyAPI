# ==========================================
# 阶段 1: 构建环境 (Builder)
# ==========================================
FROM golang:1.24-alpine AS builder

# 接收 GitHub Actions buildx 注入的目标架构参数 (amd64 或 arm64)
ARG TARGETARCH

WORKDIR /app

# 复制依赖文件并下载 (利用 Docker 缓存)
COPY go.mod go.sum* ./
RUN go mod download

# 复制所有源代码
COPY . .

# 动态交叉编译：根据 TARGETARCH 自动编译对应架构的静态二进制文件
# 彻底移除了硬编码的 arm64，完美支持多架构构建
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o autoclaw2api ./cmd/autoclaw2api

# ==========================================
# 阶段 2: 运行环境 (Runtime)
# ==========================================
FROM alpine:latest

# 安装根证书和时区数据
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# 从 builder 阶段复制编译好的二进制文件
COPY --from=builder /app/autoclaw2api .

# 复制示例配置文件供参考
COPY config.example.json ./config.example.json

# 【已移除 EXPOSE 指令】
# 端口暴露完全由你运行时的 docker run -p 或 docker-compose.yml 决定，互不干扰。

# 设置默认时区
ENV TZ=Asia/Shanghai

# 启动命令
CMD ["./autoclaw2api", "-config", "config.json"]