#!/bin/sh

APP_DIR="/usr/share/tunnelexit"
DATA_DIR="/etc/tunnelexit"
BIN="$APP_DIR/tunnelexit-server"

# 启动脚本自身的日志
LOG_DIR="$APP_DIR/logs"
LOG_FILE="$LOG_DIR/start.log"

PERSISTENT_FILES="server.json clients.json keys logs"

# 移动存在的持久化文件或目录，首次启动缺失时静默跳过
move_data_file() {
	source="$1"
	target="$2"
	if [ ! -e "$source" ]; then
		return 0
	fi
	# 搬目录前先清掉同名目标，避免目录嵌套
	if [ -d "$source" ]; then
		rm -rf "$target"
	fi
	mv -f "$source" "$target"
}

# 启动前恢复用户数据
sync_data_to_app() {
	mkdir -p "$DATA_DIR" || return 1
	for name in $PERSISTENT_FILES; do
		move_data_file "$DATA_DIR/$name" "$APP_DIR/$name" || return 1
	done
	return 0
}

# 停止后保存用户数据
sync_data_from_app() {
	mkdir -p "$DATA_DIR" || return 1
	for name in $PERSISTENT_FILES; do
		move_data_file "$APP_DIR/$name" "$DATA_DIR/$name" || true
	done
	return 0
}

if [ ! -f "$BIN" ]; then
	echo "未找到隧道服务端: $BIN" >&2
	exit 1
fi
chmod +x "$BIN" 2>/dev/null || true

sync_data_to_app || exit 1

export TUNNELEXIT_APP_DIR="$APP_DIR"
export PACK_PLATFORM="openwrt"

mkdir -p "$LOG_DIR" 2>/dev/null || true

"$BIN" >> "$LOG_FILE" 2>&1 &
APP_PID=$!

# 将 procd 的停止信号转发给 Go 主进程
forward_signal() {
	kill -TERM "$APP_PID" 2>/dev/null || true
}

trap forward_signal TERM INT

# 等待 Go 主进程真正退出，避免搬走仍在写入的配置文件
while kill -0 "$APP_PID" 2>/dev/null; do
	wait "$APP_PID"
done
APP_STATUS=$?

sync_data_from_app
exit "$APP_STATUS"
