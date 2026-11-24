#!/bin/sh
# =============================================================================
# AutoClaw2API 状态层：把 /app 下的 config.json、auths/、data/ 当成一个整体，
# 以单个 tar.gz 快照的形式同步到 S3 兼容对象存储（R2 / B2 / MinIO ...）。
#
# 设计要点：
#   1. 启动前先拉取快照，拉取失败就退出，绝不让空目录覆盖线上备份；
#   2. 运行期每 STATE_SYNC_INTERVAL 秒推一次完整快照，另存一份按天归档；
#   3. 收到 SIGTERM 后先停应用、等它写完磁盘，再推最后一次。
# 应用所有落盘都是「临时文件 + rename」，所以任意时刻拍的快照都不会是半个文件。
# =============================================================================
set -eu

STATE_DIR="${STATE_DIR:-/app}"
REMOTE="${STATE_REMOTE:-}"
INTERVAL="${STATE_SYNC_INTERVAL:-60}"
ALLOW_EMPTY="${ALLOW_EMPTY:-0}"

log() { printf '[state-sync] %s\n' "$*"; }

# Orkestr 等 PaaS 会注入 PORT，应用默认监听 8080，这里跟随平台端口
if [ -n "${PORT:-}" ]; then
	set -- "$@" -listen "0.0.0.0:${PORT}"
	log "检测到 PORT=${PORT}，应用监听 0.0.0.0:${PORT}"
fi

# 未配置远端时退化为纯本地临时存储（容器重启即丢数据）
if [ -z "$REMOTE" ]; then
	log "未设置 STATE_REMOTE，跳过云同步（容器重启将丢失全部状态）"
	exec "$@"
fi

push_state() {
	# 每次用独立临时目录，定时推送与退出推送即使重叠也不会写坏同一个包
	workdir=$(mktemp -d /tmp/state.XXXXXX) || return 1
	work="$workdir/state.tar.gz"

	# tar 退出码 1 表示「读取过程中文件有变化」的警告，快照本身仍可用
	tar_rc=0
	tar -C "$STATE_DIR" --exclude='*.tmp' --exclude='./config.example.json' -czf "$work" . 2>/dev/null || tar_rc=$?
	if [ "$tar_rc" -ne 0 ] && [ "$tar_rc" -ne 1 ]; then
		rm -rf "$workdir"
		return 1
	fi
	if [ ! -s "$work" ]; then
		rm -rf "$workdir"
		return 1
	fi

	# 先推主快照（唯一真相），成功后再写当天归档
	if ! rclone copyto --retries 3 --low-level-retries 5 "$work" "$REMOTE/state.tar.gz"; then
		rm -rf "$workdir"
		return 1
	fi
	rclone copyto --retries 1 "$work" "$REMOTE/daily/$(date +%F).tar.gz" >/dev/null 2>&1 || true
	log "已推送快照 $(stat -c %s "$work") 字节"
	rm -rf "$workdir"
}

if [ "$ALLOW_EMPTY" = "1" ]; then
	log "ALLOW_EMPTY=1：跳过启动拉取（仅用于首次初始化）"
else
	log "正在从 $REMOTE 拉取快照…"
	workdir=$(mktemp -d /tmp/state.XXXXXX)
	work="$workdir/state.tar.gz"
	if ! rclone copyto --retries 3 --low-level-retries 5 "$REMOTE/state.tar.gz" "$work"; then
		rm -rf "$workdir"
		log "拉取失败：首次部署请设置 ALLOW_EMPTY=1；已部署过请检查对象存储凭据与网络"
		exit 1
	fi
	tar -C "$STATE_DIR" -xzf "$work"
	log "状态已恢复（$(stat -c %s "$work") 字节）"
	rm -rf "$workdir"
fi

# 定时推送循环
(
	while :; do
		sleep "$INTERVAL"
		push_state || log "定时推送失败，下轮重试"
	done
) &
LOOP_PID=$!

# 启动容器原本的 CMD（autoclaw2api -config config.json ...）
"$@" &
APP_PID=$!
trap 'kill -TERM "$APP_PID" 2>/dev/null || true' TERM INT

# 启动后立刻推一次，避免「起来就崩、还没来得及落盘」
push_state || log "启动后首次推送失败"

# wait 可能被信号打断，循环到子进程真正退出为止
while :; do
	if wait "$APP_PID"; then app_rc=0; else app_rc=$?; fi
	if kill -0 "$APP_PID" 2>/dev/null; then continue; fi
	break
done

kill "$LOOP_PID" 2>/dev/null || true
wait "$LOOP_PID" 2>/dev/null || true
push_state || log "退出前推送失败"

exit "$app_rc"
