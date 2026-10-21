'use strict';
'require view';
'require fs';

// 配置与日志都在客户端自带的配置页里，本页只做跳转
// 跳转地址取自 client.json 的 webPort 与 webPath，文件缺失或损坏时退回默认值
// 需从局域网打开时改 webHost 并写入 web.token
return view.extend({
	// 读取程序目录下的客户端配置，读不到时交给默认值处理
	load: function() {
		return L.resolveDefault(fs.read('/usr/share/tunnelexit-client/client.json'), null).catch(function() { return null; });
	},

	// 把挂载路径统一成以斜杠开头与结尾，便于与主机名拼出完整地址
	normalizeWebPath: function(value) {
		var path = (typeof value === 'string' && value.length) ? value : '/';
		if (path.charAt(0) !== '/') {
			path = '/' + path;
		}
		if (path.charAt(path.length - 1) !== '/') {
			path = path + '/';
		}
		return path;
	},

	// 解析 client.json 中的配置页端口与挂载路径，非法取值退回默认值
	parseEndpoint: function(raw) {
		var config = null;
		if (raw) {
			try {
				config = JSON.parse(raw) || null;
			}
			catch (e) {
				config = null;
			}
		}
		if (!config) {
			config = {};
		}
		var port = Number(config.webPort);
		return {
			port: (port > 0) ? port : 7003,
			path: this.normalizeWebPath(config.webPath)
		};
	},

	// 跳到客户端自带的配置页，路由器管理地址与配置页共用同一个主机名
	render: function(raw) {
		var endpoint = this.parseEndpoint(raw);
		var target = window.location.protocol + '//' + window.location.hostname + ':' + endpoint.port + endpoint.path;
		window.location.replace(target);
		return E('p', {}, [ _('正在打开 TunnelExit 客户端配置页…') ]);
	},

	// 本页只做跳转，无需保存与重置处理
	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});