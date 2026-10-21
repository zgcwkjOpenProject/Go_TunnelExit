package com.zgcwkj.tunnelexit;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.os.IBinder;
import java.io.BufferedReader;
import java.io.BufferedWriter;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStreamWriter;
import java.nio.charset.StandardCharsets;

// 隧道客户端前台服务
//
// Go 客户端以可执行文件形式放在应用的 native 库目录里，本服务负责拉起它、把配置写到运行目录、
// 并把标准输出重定向到日志文件
public final class TunnelService extends Service {
    // 通知渠道标识 Android 8.0 起前台服务必须挂在渠道上
    private static final String CHANNEL_ID = "tunnelexit_client";
    // 通知编号 固定值即可 重复使用会覆盖旧通知
    private static final int NOTIFICATION_ID = 1080;

    // 当前运行中的客户端进程 null 表示未启动
    private static volatile Process process;
    // 最近一次启动失败的原因 供界面展示
    private static volatile String lastError = "";

    // 服务绑定时返回空，本服务只做进程托管不做跨进程通信
    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    // 启动时创建通知渠道并把自身提升为前台服务
    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        createChannel();
        startForeground(NOTIFICATION_ID, buildNotification());
        if (process == null || !process.isAlive()) {
            new Thread(this::launch, "tunnelexit-launch").start();
        }
        // 被系统回收后自动重建，保证隧道不会因为界面关闭而断开
        return START_STICKY;
    }

    // 停止服务时结束客户端进程
    @Override
    public void onDestroy() {
        stop();
        super.onDestroy();
    }

    // 判断客户端是否正在运行
    public static boolean isRunning() {
        Process current = process;
        return current != null && current.isAlive();
    }

    // 读取最近一次启动失败原因 没有失败时返回空字符串
    public static String lastError() {
        return lastError;
    }

    // 结束当前客户端进程
    //
    // 先发 SIGTERM 让 Go 侧走正常的退出流程，超时后再强杀
    public static void stop() {
        Process current = process;
        process = null;
        if (current == null) {
            return;
        }
        current.destroy();
        try {
            if (!current.waitFor(3, java.util.concurrent.TimeUnit.SECONDS)) {
                current.destroyForcibly();
            }
        } catch (InterruptedException e) {
            current.destroyForcibly();
            Thread.currentThread().interrupt();
        }
    }

    // 启动客户端进程并等待其结束 运行在独立线程中，因为等待进程退出是阻塞操作
    private void launch() {
        lastError = "";
        try {
            File appDir = TunnelConfig.ensureAppDir(this);
            TunnelConfig.writeClientConfig(this);
            // 启动脚本自身的日志
            File logDir = new File(appDir, "logs");
            if (!logDir.exists() && !logDir.mkdirs()) {
                throw new IOException("无法创建日志目录: " + logDir);
            }
            File logFile = new File(logDir, "start.log");
            String exe = new File(getApplicationInfo().nativeLibraryDir, "libtunnelexit-client.so").getAbsolutePath();

            ProcessBuilder builder = new ProcessBuilder(exe);
            builder.directory(appDir);
            builder.redirectErrorStream(true);
            builder.environment().put("TUNNELEXIT_APP_DIR", appDir.getAbsolutePath());
            builder.environment().put("PACK_PLATFORM", "android");
            Process started = builder.start();
            process = started;

            // 把子进程输出同时写日志与控制台（logcat），便于界面和 adb 联合排查
            try (BufferedReader reader = new BufferedReader(
                    new InputStreamReader(started.getInputStream(), StandardCharsets.UTF_8));
                 BufferedWriter writer = new BufferedWriter(
                         new OutputStreamWriter(new FileOutputStream(logFile, true), StandardCharsets.UTF_8))) {
                String line;
                while ((line = reader.readLine()) != null) {
                    writer.write(line);
                    writer.newLine();
                    writer.flush();
                    TunnelLog.append(line);
                }
            }
            int code = started.waitFor();
            lastError = "客户端已退出，退出码 " + code;
        } catch (Exception e) {
            lastError = "启动失败：" + e.getMessage();
            TunnelLog.append(lastError);
        } finally {
            process = null;
            stopForeground(STOP_FOREGROUND_REMOVE);
            stopSelf();
        }
    }

    // 创建低打扰的通知渠道，隧道运行期间只显示一条常驻通知
    private void createChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return;
        }
        NotificationManager manager = getSystemService(NotificationManager.class);
        if (manager == null || manager.getNotificationChannel(CHANNEL_ID) != null) {
            return;
        }
        NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID, "TunnelExit 客户端", NotificationManager.IMPORTANCE_LOW);
        channel.setShowBadge(false);
        manager.createNotificationChannel(channel);
    }

    // 构造前台服务通知，点击后回到主界面
    private Notification buildNotification() {
        Intent intent = new Intent(this, MainActivity.class);
        PendingIntent pending = PendingIntent.getActivity(
                this, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        Notification.Builder builder = Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        return builder
                .setContentTitle("TunnelExit")
                .setContentText("客户端正在运行")
                .setSmallIcon(R.drawable.ic_tunnelexit)
                .setContentIntent(pending)
                .setOngoing(true)
                .build();
    }

    // 启动客户端服务
    public static void start(Context context) {
        Intent intent = new Intent(context, TunnelService.class);
        context.startForegroundService(intent);
    }

    // 停止客户端服务
    public static void shutdown(Context context) {
        context.stopService(new Intent(context, TunnelService.class));
        stop();
    }
}
