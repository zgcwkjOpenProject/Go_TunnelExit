Ext.define('SYNO.SDS.TunnelExit.Instance', {
  extend: 'SYNO.SDS.AppInstance',
  appWindowName: 'SYNO.SDS.TunnelExit.Main',

  // 初始化 DSM 桌面应用实例
  constructor: function () {
    this.callParent(arguments);
  }
});

Ext.define('SYNO.SDS.TunnelExit.Main', {
  extend: 'SYNO.SDS.AppWindow',
  appInstance: null,

  // 创建承载 TunnelExit 配置页的 DSM 窗口
  constructor: function (cfg) {
    this.appInstance = cfg.appInstance;

    // iframe 走 Webman 路径，与 nginx 反代前缀保持一致
    var config = Ext.apply({
      title: 'TunnelExit',
      resizable: true,
      maximizable: true,
      minimizable: true,
      width: 1080,
      height: 760,
      minWidth: 720,
      minHeight: 520,
      layout: 'fit',
      items: [
        new Ext.BoxComponent({
          height: '100%',
          html: '<iframe title="TunnelExit" src="about:blank" frameborder="0" width="100%" height="100%" style="display:block"></iframe>',
          listeners: {
            // iframe 准备完成后取得当前 DSM 会话的 Token 再进入配置页
            afterrender: function (component) {
              var request = new XMLHttpRequest();
              request.open('GET', '/webman/login.cgi', true);
              request.timeout = 10000;

              // 将 DSM 返回的 Token 附加到首次页面请求中
              request.onload = function () {
                if (request.status !== 200) return;
                try {
                  var data = JSON.parse(request.responseText);
                  if (!data.SynoToken) return;
                  component.getEl().down('iframe').dom.src = '/webman/zgcwkj/tunnelexit-client/?SynoToken=' + encodeURIComponent(data.SynoToken);
                } catch (_) {
                  return;
                }
              };
              request.send();
            }
          }
        })
      ]
    }, cfg);

    this.callParent([config]);
  }
});