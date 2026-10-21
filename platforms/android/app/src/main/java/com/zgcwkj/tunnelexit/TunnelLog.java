package com.zgcwkj.tunnelexit;

import java.util.ArrayDeque;
import java.util.Deque;

// 内存日志环形缓冲
//
// 客户端进程的标准输出既写文件也走这里，界面切换回来时可以立刻看到最近日志
public final class TunnelLog {
    // 最多保留的日志行数 避免长时间运行占用过多内存
    private static final int MAX_LINES = 400;
    // 环形缓冲本体 仅在 synchronized 块中访问
    private static final Deque<String> LINES = new ArrayDeque<>();

    // 工具类不需要实例化
    private TunnelLog() {
    }

    // 追加一行日志
    public static void append(String line) {
        synchronized (LINES) {
            LINES.addLast(line);
            while (LINES.size() > MAX_LINES) {
                LINES.removeFirst();
            }
        }
    }

    // 导出当前日志快照 以换行连接
    public static String snapshot() {
        synchronized (LINES) {
            return String.join("\n", LINES);
        }
    }

    // 清空日志缓冲
    public static void clear() {
        synchronized (LINES) {
            LINES.clear();
        }
    }
}
