#!/bin/sh
# 群晖以套件用户运行本脚本，脚本只负责把用户数据在两个目录之间搬运后拉起服务端

APP_DIR="/var/packages/tunnelexit/target/app"
PKGVAR="/var/packages/tunnelexit/var"
DATA_DIR="$PKGVAR/app"

BIN="$APP_DIR/tunnelexit-server"
CONFIG_FILE="server.json"
STORE_FILE="clients.json"
KEYS_DIR="keys"
# 启动脚本自身的日志
LOG_DIR="logs"
LOG_FILE="$LOG_DIR/start.log"

# 需要跨升级保留的运行时文件与目录
# 密钥目录必须保留，重新生成会让客户端公钥失效
PERSISTENT_FILES="$CONFIG_FILE $STORE_FILE $KEYS_DIR $LOG_DIR"

if [ ! -f "$BIN" ]; then
    echo "ERROR: 未找到二进制: $BIN" >&2
    exit 1
fi

mkdir -p "$DATA_DIR" 2>/dev/null || {
    echo "ERROR: 无法创建数据目录: $DATA_DIR" >&2
    exit 1
}

# 确保二进制具有执行权限
chmod +x "$BIN" 2>/dev/null || true

# 移动单个文件或目录，源不存在时跳过
move_file() {
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

# 启动前把用户数据目录中的配置恢复到程序目录
for name in $PERSISTENT_FILES; do
    if [ -e "$DATA_DIR/$name" ]; then
        move_file "$DATA_DIR/$name" "$APP_DIR/$name" || exit 1
        # 目录保留可进入权限，程序才能在里面对日志归档
        if [ -d "$APP_DIR/$name" ]; then
            chmod u+rwx "$APP_DIR/$name" 2>/dev/null || true
        else
            chmod u+rw "$APP_DIR/$name" 2>/dev/null || true
        fi
    fi
done

# TUNNELEXIT_APP_DIR 让配置与日志固定落在程序目录，与安装升级的搬运逻辑一致
export TUNNELEXIT_APP_DIR="$APP_DIR"
export PACK_PLATFORM="synology"

mkdir -p "$APP_DIR/$LOG_DIR" 2>/dev/null || true

"$BIN" >> "$APP_DIR/$LOG_FILE" 2>&1 &
APP_PID=$!

# 停止后把程序目录中的用户数据保存回数据目录
sync_data_from_app() {
    for name in $PERSISTENT_FILES; do
        if [ -e "$APP_DIR/$name" ]; then
            move_file "$APP_DIR/$name" "$DATA_DIR/$name" || true
        fi
    done
}

# 将停止信号转发给 Go 主进程
forward_signal() {
    kill -TERM "$APP_PID" 2>/dev/null || true
}

trap forward_signal TERM INT

# 等待子进程实际退出，避免信号中断 wait 后提前搬运仍在写入的配置
APP_STATUS=0
while kill -0 "$APP_PID" 2>/dev/null; do
    wait "$APP_PID"
    APP_STATUS=$?
done

sync_data_from_app
exit "$APP_STATUS"
