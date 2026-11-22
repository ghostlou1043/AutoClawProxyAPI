# ==========================================
# 阶段 1: 构建环境 (Builder)
# ==========================================
FROM golang:1.24-alpine AS builder

ARG TARGETARCH
WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o autoclaw2api ./cmd/autoclaw2api

# ==========================================
# 阶段 2: 运行环境 (Runtime)
# ==========================================
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

# 【修改点 1】：工作目录保持 /app，用于存放数据（config.json, auths, data）
WORKDIR /app

# 【修改点 2】：把二进制文件复制到 /usr/local/bin，而不是 /app
# 这样它就在系统 PATH 里，且不会被 volume 挂载覆盖
COPY --from=builder /app/autoclaw2api /usr/local/bin/autoclaw2api

COPY config.example.json ./config.example.json

ENV TZ=Asia/Shanghai

# 【修改点 3】：直接写命令名，不需要 ./ 了，因为它在 PATH 里
CMD ["autoclaw2api", "-config", "config.json"]
