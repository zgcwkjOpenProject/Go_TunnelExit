package com.zgcwkj.tunnelexit;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

// 开机自启接收器
//
// 系统启动完成后收到 BOOT_COMPLETED 广播，配置完整时自动拉起隧道前台服务
public final class BootReceiver extends BroadcastReceiver {
    // 收到开机广播后校验配置完整性，不完整时静默跳过等待用户手动填写
    @Override
    public void onReceive(Context context, Intent intent) {
        if (!Intent.ACTION_BOOT_COMPLETED.equals(intent.getAction())) {
            return;
        }
        String host = TunnelConfig.serverHost(context);
        String name = TunnelConfig.clientName(context);
        String serverKey = TunnelConfig.serverKey(context);
        if (host.isEmpty() || name.isEmpty() || serverKey.isEmpty()) {
            return;
        }
        TunnelLog.clear();
        TunnelLog.append("开机自启：检测到完整配置，自动启动客户端 → " + host);
        try {
            TunnelService.start(context);
        } catch (Exception e) {
            TunnelLog.append("开机自启失败：" + e.getMessage());
        }
    }
}
