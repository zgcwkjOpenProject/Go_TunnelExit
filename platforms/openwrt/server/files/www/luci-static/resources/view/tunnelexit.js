'use strict';
'require view';
'require uci';
'require fs';
'require ui';

// TunnelExit 的 LuCI 页面：仅展示运行状态与端口映射，配置由 /etc/config/tunnelexit 管理
return view.extend({
	// 读取节点记录文件，失败时返回空值
	load: function() {
		return Promise.all([
			uci.load('tunnelexit'),
			L.resolveDefault(fs.read('/usr/share/tunnelexit/clients.json'), null).catch(function() { return null; })
		]);
	},

	// 解析 clients.json，内容为「客户端唯一 ID -> 端口与流量记录」的映射
	parseClients: function(raw) {
		if (!raw) {
			return [];
		}
		var data = null;
		try {
			data = JSON.parse(raw);
		}
		catch (e) {
			return [];
		}
		var result = [];
		Object.keys(data || {}).forEach(function(id) {
			var record = data[id] || {};
			result.push({
				id: id,
				address: record.address || '',
				name: record.name || '',
				port: Number(record.port) || 0,
				up: Number(record.up) || 0,
				down: Number(record.down) || 0,
				lastSeen: record.lastSeen || ''
			});
		});
		result.sort(function(a, b) { return a.port - b.port; });
		return result;
	},

	// 把累计字节数格式化成可读体积
	formatBytes: function(value) {
		var size = Number(value) || 0;
		var units = ['B', 'KB', 'MB', 'GB', 'TB'];
		var index = 0;
		while (size >= 1024 && index < units.length - 1) {
			size = size / 1024;
			index++;
		}
		return ((index === 0) ? String(size) : size.toFixed(2)) + ' ' + units[index];
	},

	// 把记录里的 ISO 时间转成本地可读文本，缺失或非法时显示未知
	formatTime: function(value) {
		if (!value) {
			return _('未知');
		}
		var date = new Date(value);
		if (isNaN(date.getTime())) {
			return _('未知');
		}
		return date.toLocaleString();
	},

	// 渲染状态卡片与端口映射表格
	render: function(data) {
		var self = this;
		var config = data[0];
		var clients = this.parseClients(data[1]);

		var statusText = (L.toArray(config.get('tunnelexit', 'server', 'enabled')).pop() === '0')
			? _('已禁用')
			: _('已启用');

		var rows = clients.map(function(item) {
			return E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td left' }, [ item.id ]),
				E('td', { 'class': 'td left' }, [ item.name || _('未命名') ]),
				E('td', { 'class': 'td left' }, [ item.address ]),
				E('td', { 'class': 'td left' }, [ String(item.port) ]),
				E('td', { 'class': 'td left' }, [ self.formatBytes(item.up) ]),
				E('td', { 'class': 'td left' }, [ self.formatBytes(item.down) ]),
				E('td', { 'class': 'td left' }, [ self.formatTime(item.lastSeen) ])
			]);
		});

		if (!rows.length) {
			rows.push(E('tr', { 'class': 'tr placeholder' }, [
				E('td', { 'class': 'td', 'colspan': 7 }, [ E('em', {}, [ _('暂无客户端接入，端口映射表将在客户端首连后生成。') ]) ])
			]));
		}

		var port = L.toArray(config.get('tunnelexit', 'server', 'port')).pop() || '9733';
		var socksBase = L.toArray(config.get('tunnelexit', 'server', 'socks_port_base')).pop() || '19733';
		var socksCount = L.toArray(config.get('tunnelexit', 'server', 'socks_port_count')).pop() || '2500';
		var webHost = L.toArray(config.get('tunnelexit', 'server', 'web_host')).pop() || '127.0.0.1';
		var webPort = L.toArray(config.get('tunnelexit', 'server', 'web_port')).pop() || '9732';
		// 展示页挂载路径统一成以斜杠开头与结尾，便于拼出完整地址
		var webPath = L.toArray(config.get('tunnelexit', 'server', 'web_path')).pop() || '/';
		if (webPath.charAt(0) !== '/') { webPath = '/' + webPath; }
		if (webPath.charAt(webPath.length - 1) !== '/') { webPath = webPath + '/'; }

		return E([
			E('h2', {}, [ _('TunnelExit') ]),
			E('div', { 'class': 'cbi-map-descr' }, [
				_('反向 SOCKS5 隧道服务端：客户端连上来后，本机为它分配一个专属 SOCKS5 端口，外部即可经由该客户端的网络访问资源。')
			]),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [ _('服务状态') ]),
				E('table', { 'class': 'table' }, [
					E('tr', { 'class': 'tr' }, [
						E('td', { 'class': 'td left', 'width': '33%' }, [ _('启动设置') ]),
						E('td', { 'class': 'td left' }, [ statusText ])
					]),
					E('tr', { 'class': 'tr' }, [
						E('td', { 'class': 'td left' }, [ _('隧道端口') ]),
						E('td', { 'class': 'td left' }, [ String(port) ])
					]),
					E('tr', { 'class': 'tr' }, [
						E('td', { 'class': 'td left' }, [ _('SOCKS5 起始端口') ]),
						E('td', { 'class': 'td left' }, [ String(socksBase) ])
					]),
					E('tr', { 'class': 'tr' }, [
						E('td', { 'class': 'td left' }, [ _('SOCKS5 端口数量') ]),
						E('td', { 'class': 'td left' }, [ String(socksCount) ])
					]),
					E('tr', { 'class': 'tr' }, [
						E('td', { 'class': 'td left' }, [ _('展示页面') ]),
						E('td', { 'class': 'td left' }, [ 'http://' + webHost + ':' + webPort + webPath ])
					])
				])
			]),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [ _('节点与流量') ]),
				E('table', { 'class': 'table' }, [
					E('tr', { 'class': 'tr table-titles' }, [
						E('th', { 'class': 'th' }, [ _('客户端 ID') ]),
						E('th', { 'class': 'th' }, [ _('客户端') ]),
						E('th', { 'class': 'th' }, [ _('出口 IP') ]),
						E('th', { 'class': 'th' }, [ _('SOCKS5 端口') ]),
						E('th', { 'class': 'th' }, [ _('上行') ]),
						E('th', { 'class': 'th' }, [ _('下行') ]),
						E('th', { 'class': 'th' }, [ _('最近在线') ])
					])
				].concat(rows))
			]),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [ _('配置入口') ]),
				E('p', {}, [
					_('监听端口与端口池在 %s 中调整，修改后执行 /etc/init.d/tunnelexit reload 生效。运行日志见 %s。')
						.format('/etc/config/tunnelexit', '/usr/share/tunnelexit/logs/tunnelexit.log')
				]),
				E('p', {}, [
					_('展示页只显示归属地与流量，不含 IP 与客户端名，服务端没有管理入口；需要外部访问时把 %s 改为 0.0.0.0，并用 option web_path 把它挂到反向代理的子路径下。')
						.format('/etc/config/tunnelexit 的 option web_host')
				])
			])
		]);
	},

	// 本页只读，无需额外处理句柄
	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
