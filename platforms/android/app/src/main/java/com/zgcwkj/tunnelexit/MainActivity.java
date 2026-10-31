package com.zgcwkj.tunnelexit;

import android.app.Activity;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.InputType;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

// 隧道客户端主界面
//
// 纯代码构建布局，不依赖任何外部 UI 库：上方是配置输入与启停按钮，下方是可滚动的运行日志
public final class MainActivity extends Activity {
    // 状态刷新间隔 单位毫秒
    private static final long REFRESH_INTERVAL_MS = 1000;
    // 通知权限请求码
    private static final int NOTIFICATION_REQUEST = 2001;

    // 主线程处理器 周期刷新界面
    private final Handler handler = new Handler(Looper.getMainLooper());
    // 输入框 服务端地址
    private EditText hostInput;
    // 输入框 客户端名称
    private EditText nameInput;
    // 输入框 服务端公钥，多行 PEM 文本，未填写时客户端不会连接
    private EditText keyInput;
    // 状态文本
    private TextView statusText;
    // 日志文本
    private TextView logText;
    // 启动按钮
    private Button startButton;
    // 停止按钮
    private Button stopButton;

    // 周期刷新任务 同步按钮可用状态 状态文字与日志
    private final Runnable refresh = new Runnable() {
        // 按固定间隔刷新界面，读取服务状态而不是自己维护状态位
        @Override
        public void run() {
            boolean running = TunnelService.isRunning();
            startButton.setEnabled(!running);
            stopButton.setEnabled(running);
            hostInput.setEnabled(!running);
            nameInput.setEnabled(!running);
            keyInput.setEnabled(!running);
            statusText.setText(running
                    ? "运行中 · 服务端 " + TunnelConfig.serverHost(MainActivity.this)
                      + ":" + TunnelConfig.prefs(MainActivity.this).getInt("port", TunnelConfig.DEFAULT_PORT)
                    : (TunnelService.lastError().isEmpty() ? "已停止" : "已停止 · " + TunnelService.lastError()));
            String text = TunnelLog.snapshot() + "\n";
            if (!text.contentEquals(logText.getText())) {
                logText.setText(text);
            }
            handler.postDelayed(this, REFRESH_INTERVAL_MS);
        }
    };

    // 构建界面、申请通知权限、按已保存配置自动启动并开启周期刷新
    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        setContentView(buildLayout());
        requestNotificationPermission();
        autoStartIfNeeded();
        handler.post(refresh);
    }

    // 已保存的配置完整时打开应用自动启动，配置不完整则继续让用户填写
    private void autoStartIfNeeded() {
        if (TunnelService.isRunning()) {
            return;
        }
        String host = TunnelConfig.serverHost(this);
        String name = TunnelConfig.clientName(this);
        String serverKey = TunnelConfig.serverKey(this);
        if (host.isEmpty() || name.isEmpty() || serverKey.isEmpty()) {
            return;
        }
        TunnelLog.clear();
        TunnelLog.append("检测到已保存的密钥，自动启动客户端 → " + host);
        try {
            TunnelService.start(this);
        } catch (Exception e) {
            TunnelLog.append("自动启动失败：" + e.getMessage());
        }
    }

    // 页面不可见时停止刷新，避免后台无谓地读日志
    @Override
    protected void onPause() {
        handler.removeCallbacks(refresh);
        super.onPause();
    }

    // 回到页面立即刷新一次，并重启周期任务
    @Override
    protected void onResume() {
        super.onResume();
        handler.removeCallbacks(refresh);
        handler.post(refresh);
    }

    // 退出时清空刷新任务，防止 Activity 泄漏
    @Override
    protected void onDestroy() {
        handler.removeCallbacks(refresh);
        super.onDestroy();
    }

    // 用代码搭建整体布局：配置区 + 按钮区 + 日志区
    private View buildLayout() {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundColor(Color.rgb(16, 24, 39));
        int pad = dp(16);
        root.setPadding(pad, pad, pad, pad);

        TextView title = new TextView(this);
        title.setText("TunnelExit · 客户端");
        title.setTextColor(Color.WHITE);
        title.setTextSize(20);
        root.addView(title);

        statusText = new TextView(this);
        statusText.setText("已停止");
        statusText.setTextColor(Color.rgb(148, 163, 184));
        statusText.setTextSize(13);
        LinearLayout.LayoutParams statusParams = new LinearLayout.LayoutParams(-1, -2);
        statusParams.topMargin = dp(6);
        root.addView(statusText, statusParams);

        hostInput = addField(root, "服务端地址", TunnelConfig.serverHost(this), InputType.TYPE_CLASS_TEXT);
        nameInput = addField(root, "客户端名称", TunnelConfig.clientName(this), InputType.TYPE_CLASS_TEXT);
        keyInput = addMultilineField(root, "服务端公钥", TunnelConfig.serverKey(this));

        LinearLayout buttons = new LinearLayout(this);
        buttons.setOrientation(LinearLayout.HORIZONTAL);
        LinearLayout.LayoutParams buttonsParams = new LinearLayout.LayoutParams(-1, -2);
        buttonsParams.topMargin = dp(12);
        startButton = new Button(this);
        startButton.setText("启动");
        startButton.setOnClickListener(v -> startTunnel());
        stopButton = new Button(this);
        stopButton.setText("停止");
        stopButton.setOnClickListener(v -> {
            TunnelService.shutdown(this);
            Toast.makeText(this, "已请求停止", Toast.LENGTH_SHORT).show();
        });
        buttons.addView(startButton, new LinearLayout.LayoutParams(0, -2, 1f));
        LinearLayout.LayoutParams stopParams = new LinearLayout.LayoutParams(0, -2, 1f);
        stopParams.leftMargin = dp(8);
        buttons.addView(stopButton, stopParams);
        root.addView(buttons, buttonsParams);

        TextView logTitle = new TextView(this);
        logTitle.setText("运行日志");
        logTitle.setTextColor(Color.WHITE);
        logTitle.setTextSize(15);
        LinearLayout.LayoutParams logTitleParams = new LinearLayout.LayoutParams(-1, -2);
        logTitleParams.topMargin = dp(18);
        root.addView(logTitle, logTitleParams);

        logText = new TextView(this);
        logText.setTextColor(Color.rgb(203, 213, 225));
        logText.setTextSize(11);
        logText.setTypeface(android.graphics.Typeface.MONOSPACE);
        ScrollView scroll = new ScrollView(this);
        scroll.addView(logText);
        LinearLayout.LayoutParams scrollParams = new LinearLayout.LayoutParams(-1, 0, 1f);
        scrollParams.topMargin = dp(6);
        root.addView(scroll, scrollParams);
        return root;
    }

    // 往根布局追加一行"标签 + 输入框"，并返回输入框引用
    private EditText addField(LinearLayout root, String label, String value, int inputType) {
        TextView title = new TextView(this);
        title.setText(label);
        title.setTextColor(Color.rgb(148, 163, 184));
        title.setTextSize(12);
        LinearLayout.LayoutParams titleParams = new LinearLayout.LayoutParams(-1, -2);
        titleParams.topMargin = dp(14);
        root.addView(title, titleParams);

        EditText input = new EditText(this);
        input.setText(value);
        input.setTextColor(Color.WHITE);
        input.setInputType(inputType);
        input.setSingleLine(true);
        root.addView(input, new LinearLayout.LayoutParams(-1, -2));
        return input;
    }

    // 往根布局追加一行"标签 + 多行输入框"，供 PEM 公钥这类含换行的文本使用
    private EditText addMultilineField(LinearLayout root, String label, String value) {
        EditText input = addField(root, label, value, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE);
        input.setSingleLine(false);
        input.setMinLines(5);
        input.setGravity(Gravity.TOP | Gravity.START);
        return input;
    }

    // 保存输入内容后拉起前台服务；参数非法时直接提示并中止
    private void startTunnel() {
        String host = hostInput.getText().toString().trim();
        String name = nameInput.getText().toString().trim();
        String serverKey = keyInput.getText().toString().trim();
        if (host.isEmpty()) {
            Toast.makeText(this, "请填写服务端地址", Toast.LENGTH_SHORT).show();
            return;
        }
        if (name.isEmpty()) {
            Toast.makeText(this, "请填写客户端名称", Toast.LENGTH_SHORT).show();
            return;
        }
        if (serverKey.isEmpty()) {
            Toast.makeText(this, "请填写服务端公钥，见服务端 keys/public_key.pem", Toast.LENGTH_LONG).show();
            return;
        }
        TunnelConfig.prefs(this).edit()
                .putString("serverHost", host)
                .putString("name", name)
                .putString("serverKey", serverKey)
                .apply();
        TunnelLog.clear();
        TunnelLog.append("准备启动客户端 → " + host);
        try {
            TunnelService.start(this);
        } catch (Exception e) {
            Toast.makeText(this, "启动失败：" + e.getMessage(), Toast.LENGTH_LONG).show();
        }
    }

    // Android 13 起前台服务通知需要显式授权，未授权时降级为普通提示
    private void requestNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            return;
        }
        if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[] { android.Manifest.permission.POST_NOTIFICATIONS }, NOTIFICATION_REQUEST);
        }
    }

    // dp 转像素
    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }
}
