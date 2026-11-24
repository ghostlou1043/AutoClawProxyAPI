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

# rclone：把 /app 状态快照同步到 R2 / B2 等 S3 兼容对象存储
RUN apk --no-cache add ca-certificates tzdata rclone

# 【修改点 1】：工作目录保持 /app，用于存放数据（config.json, auths, data）
WORKDIR /app

# 【修改点 2】：把二进制文件复制到 /usr/local/bin，而不是 /app
# 这样它就在系统 PATH 里，且不会被 volume 挂载覆盖
COPY --from=builder /app/autoclaw2api /usr/local/bin/autoclaw2api

COPY config.example.json ./config.example.json

ENV TZ=Asia/Shanghai

# 【修改点 3】：直接写命令名，不需要 ./ 了，因为它在 PATH 里
# 【修改点 4】：入口改为状态同步脚本 —— 启动前拉取快照、运行期定时推送、退出前再推一次
# tr 兜底把可能的 CRLF 换成 LF，避免在 Linux 上因 shebang 带 \r 启动失败
COPY ops/state-sync.sh /usr/local/bin/state-sync.sh
RUN tr -d '\r' < /usr/local/bin/state-sync.sh > /tmp/state-sync.sh \
 && mv /tmp/state-sync.sh /usr/local/bin/state-sync.sh \
 && chmod 0755 /usr/local/bin/state-sync.sh

ENTRYPOINT ["/usr/local/bin/state-sync.sh"]
CMD ["autoclaw2api", "-config", "config.json"]
