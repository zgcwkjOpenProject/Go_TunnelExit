package com.zgcwkj.tunnelexit;

import android.content.Context;
import android.content.SharedPreferences;
import java.io.File;
import java.io.FileOutputStream;
import java.io.OutputStreamWriter;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;

// 客户端配置读写
//
// 界面上的设置保存在 SharedPreferences 中，启动前再渲染成 Go 客户端能识别的 client.json 写入运行目录
public final class TunnelConfig {
    // 配置文件所在目录名 位于应用私有 files 目录下
    private static final String APP_DIR_NAME = "tunnelexit";
    // SharedPreferences 文件名
    private static final String PREFS = "tunnelexit_settings";
    // 默认服务端地址 与 Go 侧默认值保持一致
    public static final String DEFAULT_SERVER_HOST = "127.0.0.1";
    // 默认隧道端口 登录与空闲数据连接共用 与 Go 侧默认值保持一致
    public static final int DEFAULT_PORT = 9733;
    // 默认客户端名称
    public static final String DEFAULT_NAME = "android";
    // 默认空闲连接数
    public static final int DEFAULT_POOL_SIZE = 16;

    // 工具类不需要实例化
    private TunnelConfig() {
    }

    // 确保运行目录存在 返回运行目录
    public static File ensureAppDir(Context context) {
        File dir = new File(context.getFilesDir(), APP_DIR_NAME);
        if (!dir.exists() && !dir.mkdirs()) {
            throw new IllegalStateException("无法创建运行目录：" + dir);
        }
        return dir;
    }

    // 打开设置存储
    public static SharedPreferences prefs(Context context) {
        return context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    // 读取服务端地址
    public static String serverHost(Context context) {
        return prefs(context).getString("serverHost", DEFAULT_SERVER_HOST);
    }

    // 读取客户端名称 仅用于服务端日志与节点记录
    public static String clientName(Context context) {
        return prefs(context).getString("name", DEFAULT_NAME);
    }

    // 读取服务端公钥 未填写时返回空串 与 Go 侧 ErrKeyMissing 对应
    public static String serverKey(Context context) {
        return prefs(context).getString("serverKey", "");
    }

    // 读取客户端唯一 ID 首次调用时生成并保存
    //
    // 服务端按该 ID 与来源 IP 分配 SOCKS5 端口，必须跨重启保持稳定，否则每次启动都会换到新端口
    public static String clientID(Context context) {
        SharedPreferences p = prefs(context);
        String id = p.getString("id", "");
        if (id != null && !id.isEmpty()) {
            return id;
        }
        byte[] seed = new byte[8];
        new SecureRandom().nextBytes(seed);
        StringBuilder hex = new StringBuilder(seed.length * 2);
        for (byte b : seed) {
            hex.append(String.format("%02x", b));
        }
        id = hex.toString();
        p.edit().putString("id", id).apply();
        return id;
    }

    // 把当前设置写成 client.json 每次整体覆写，避免旧字段残留影响 Go 侧解析
    public static void writeClientConfig(Context context) {
        File dir = ensureAppDir(context);
        SharedPreferences p = prefs(context);
        String json = "{\n"
                + "  \"serverHost\": \"" + escape(serverHost(context)) + "\",\n"
                + "  \"port\": " + p.getInt("port", DEFAULT_PORT) + ",\n"
                + "  \"serverKey\": \"" + escape(serverKey(context)) + "\",\n"
                + "  \"id\": \"" + escape(clientID(context)) + "\",\n"
                + "  \"name\": \"" + escape(clientName(context)) + "\",\n"
                + "  \"poolSize\": " + p.getInt("poolSize", DEFAULT_POOL_SIZE) + "\n"
                + "}\n";
        File target = new File(dir, "client.json");
        try (OutputStreamWriter writer = new OutputStreamWriter(
                new FileOutputStream(target), StandardCharsets.UTF_8)) {
            writer.write(json);
        } catch (Exception e) {
            throw new IllegalStateException("写入 client.json 失败：" + e.getMessage(), e);
        }
    }

    // 转义 JSON 字符串里的反斜杠、引号与控制字符，避免用户输入破坏配置文件结构
    //
    // 服务端公钥是多行 PEM 文本，换行必须转义成 \n，否则写出的 JSON 无法被 Go 侧解析
    private static String escape(String value) {
        return value.replace("\\", "\\\\")
                .replace("\"", "\\\"")
                .replace("\r", "\\r")
                .replace("\n", "\\n")
                .replace("\t", "\\t");
    }
}
