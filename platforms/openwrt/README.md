# TunnelExit OpenWrt 软件包

本目录提供 OpenWrt 的 procd 启动外壳，按角色分 `server/` 与 `client/` 两套，两者各带一个 LuCI 入口。
业务代码来自 `common/`，由 `platforms/build-openwrt.ps1` 交叉编译并组装为 `.ipk`。

服务端包让路由器充当地道入口，另带 UCI 配置与 LuCI 状态页；客户端包让路由器借用别的机器的网络出口，
配置以 `client.json` 为准，LuCI 里只放一个跳到客户端自带配置页的入口。两者可以同装也可以只装一个。

## 目录结构

```text
platforms/openwrt/
├── README.md
├── server/files/                       服务端包安装到设备根目录的文件树
│   ├── etc/
│   │   ├── config/tunnelexit           UCI 配置：端口、监听地址、端口范围、展示页
│   │   └── init.d/tunnelexit           procd 服务，安装后默认开机启动
│   ├── usr/share/
│   │   ├── tunnelexit/run.sh           运行包装脚本，负责搬运用户数据
│   │   ├── luci/menu.d/tunnelexit.json LuCI 状态页入口
│   │   └── rpcd/acl.d/luci-app-tunnelexit.json  LuCI 状态页所需的读权限
│   └── www/luci-static/resources/view/tunnelexit.js  状态页：只读展示节点、归属地与流量
└── client/files/                      客户端包安装到设备根目录的文件树
    ├── etc/init.d/tunnelexit-client   procd 服务，无 UCI 配置
    ├── usr/share/
    │   ├── tunnelexit-client/run.sh   运行包装脚本，负责搬运用户数据
    │   ├── luci/menu.d/tunnelexit-client.json   LuCI 配置页入口
    │   └── rpcd/acl.d/luci-app-tunnelexit-client.json  跳转页读取 client.json 所需的读权限
    └── www/luci-static/resources/view/tunnelexit-client.js  跳转页：打开客户端配置页
```

安装后设备上的布局：

| 路径 | 角色 | 说明 |
| --- | --- | --- |
| `/usr/share/tunnelexit/tunnelexit-server` | 服务端 | 服务端二进制（构建时注入） |
| `/usr/share/tunnelexit/ip2region_v4.xdb` | 服务端 | IPv4 离线归属地库（构建时注入，`TUNNELEXIT_SKIP_GEOIP=1` 时不打包） |
| `/usr/share/tunnelexit/ip2region_v6.xdb` | 服务端 | IPv6 离线归属地库（构建时注入，`TUNNELEXIT_SKIP_GEOIP=1` 或 `TUNNELEXIT_SKIP_GEOIP_V6=1` 时不打包） |
| `/usr/share/tunnelexit/run.sh` | 服务端 | 包装脚本，procd 直接监控它 |
| `/etc/init.d/tunnelexit` | 服务端 | procd 服务，`enable` 后开机自启 |
| `/etc/config/tunnelexit` | 服务端 | UCI 配置，`conffiles` 声明，升级保留本地修改 |
| `/etc/tunnelexit/` | 服务端 | 持久化目录：`server.json`、`clients.json`、`keys/`、`logs/` |
| `/www/luci-static/resources/view/tunnelexit.js` | 服务端 | LuCI 状态页 |
| `/usr/share/tunnelexit-client/tunnelexit-client` | 客户端 | 客户端二进制（构建时注入） |
| `/usr/share/tunnelexit-client/run.sh` | 客户端 | 包装脚本，procd 直接监控它 |
| `/etc/init.d/tunnelexit-client` | 客户端 | procd 服务，`enable` 后开机自启 |
| `/etc/tunnelexit-client/` | 客户端 | 持久化目录：`client.json`、`logs/`，可选 `web.token` |
| `/www/luci-static/resources/view/tunnelexit-client.js` | 客户端 | LuCI 入口：跳到客户端自带的配置页 |
| `/usr/share/rpcd/acl.d/luci-app-tunnelexit-client.json` | 客户端 | LuCI 入口读取 `client.json` 的读权限 |

## 端口与配置

服务端对外只需要三个端口，路由器通常还要在防火墙上放行；
客户端只主动连出服务端的隧道端口，不需在防火墙上开口。

| 端口 | 用途 |
| --- | --- |
| 9733 | 隧道端口：客户端登录与预建的空闲连接共用 |
| 19733+ | SOCKS5 端口：按客户端 ID 与来源 IP 分配并持久化，共 `socks_port_count` 个 |
| 9732 | 展示页端口，默认只监听 `127.0.0.1` |

`/etc/config/tunnelexit` 中的值通过环境变量下发给 Go 进程，与
`common/app` 的 `applyServerEnv` 一一对应，因此不依赖额外的配置文件生成逻辑：

```sh
uci set tunnelexit.server.port='9733'
uci set tunnelexit.server.socks_port_base='20733'
uci set tunnelexit.server.socks_port_count='200'
uci commit tunnelexit
/etc/init.d/tunnelexit reload
```

单次修改也可直接用 `reload_service`，它会先 `stop` 再 `start`，让 procd 重新读取 UCI。

客户端包不引入 UCI，避免出现第二份配置来源。安装后先改 `/etc/tunnelexit-client/client.json`
的 `serverHost` 与 `serverKey`，否则客户端连不上服务端：

```sh
mkdir -p /etc/tunnelexit-client
printf '%s\n' '{"serverHost":"服务器地址","port":9733,"serverKey":"","poolSize":16,"webHost":"127.0.0.1","webPort":7003,"webPath":"/"}' > /etc/tunnelexit-client/client.json
/etc/init.d/tunnelexit-client restart
```

`serverKey` 填服务端 `keys/public_key.pem` 里的公钥文本（含首尾行，JSON 里换行写成 `\n`）；
隧道端口只接受 TLS 连接，公钥不对就无法建立隧道。`client.json` 也可以交给客户端自带的配置页修改，两者改的是同一份文件；配置页里保存后立即生效、无需重启服务；目录里已有该文件时不要覆盖，
直接改其中的 `serverHost` 与 `serverKey` 即可保留原有的客户端唯一 ID。

## 展示页面与配置页面

服务端只在 `web_port`（默认 9732）上提供一个只读的节点展示页，没有管理入口也没有配置接口：

| 路径 | 内容 |
| --- | --- |
| `/` | 匿名展示页：节点状态、归属地、SOCKS5 端口与上下行流量，不含 IP 与客户端名 |
| `/api/nodes` | 展示页请求一次的匿名节点列表，字段与页面一致 |
| `/api/stream` | 展示页实际使用的 SSE 接口，每秒推送一次带即时速率的匿名节点列表 |

展示页不含 IP 与客户端名，可以直接给外部看；服务端参数只在 `/etc/config/tunnelexit` 中调整。
`web_host` 默认 `127.0.0.1`，因此只能在路由器自身访问，需要从局域网查看时：

```sh
uci set tunnelexit.server.web_host='0.0.0.0'
uci commit tunnelexit
/etc/init.d/tunnelexit reload
```

把 `web_path` 设成 `/tunnelexit` 之类的值后，展示页会挂到对应子路径下，便于经反向代理访问。

客户端在 `web_port`（默认 7003）上提供配置管理页，根路径就是配置页，可修改服务端地址、隧道端口与服务端公钥并查看日志：

| 路径 | 内容 |
| --- | --- |
| `/` | 配置页：修改 `client.json` 中的参数、查看运行日志 |
| `/api/config` | 读取与保存配置的接口 |
| `/api/log` | 读取运行日志的接口 |

页面与接口受 `TUNNELEXIT_WEB_TOKEN` 保护，不设口令时不做校验。OpenWrt 客户端把口令放在
`/etc/tunnelexit-client/web.token`（第一行，不换行），有该文件时 `run.sh` 会把它作为口令传入；
默认只监听 `127.0.0.1`，需要从局域网打开时把 `client.json` 的 `webHost` 改成 `0.0.0.0` 并设置口令。

LuCI 里的入口分两处：服务端的 **服务 → TunnelExit** 是内置状态页，只读展示节点与流量；
客户端的 **服务 → TunnelExit 客户端** 只做跳转，把浏览器带到 `client.json` 里 `webPort` 与 `webPath`
拼出的地址（默认 `http://<路由器地址>:7003/`），因此客户端必须把 `webHost` 改成 `0.0.0.0`
并写入 `web.token`，否则跳转后页面打不开。

## 数据持久化

`run.sh` 在启动前把持久化目录中的文件搬进程序目录，停止后再搬回。这样 `opkg` 覆盖安装
`/usr/share/` 时不会丢配置与节点记录，重启后同一客户端仍拿到原来的 SOCKS5 端口。

服务端在 `/etc/tunnelexit/` 与 `/usr/share/tunnelexit/` 之间搬 `server.json`、`clients.json`、`keys/` 与 `logs/`；
客户端在 `/etc/tunnelexit-client/` 与 `/usr/share/tunnelexit-client/` 之间搬 `client.json` 与 `logs/`。
服务端首次启动会在 `keys/` 里生成 `private_key.pem`、`server.crt` 与 `public_key.pem`，该目录必须随升级保留，否则公钥变化会让所有客户端连不上。
程序日志在 `logs/tunnelexit.log`，启动脚本日志在 `logs/start.log`；程序日志按天归档成 `logs/tunnelexit-日期.log` 并默认保留 3 天，归档文件随 `logs/` 一起搬运，覆盖安装后仍能回看。
`server.json` 的 `nodeKeepDays` 设为正数后，超过该天数未在线的客户端记录会在启动与运行中自动清理，默认 `0` 表示不删除。

`clients.json` 以客户端唯一 ID 为键，保存来源 IP、端口、客户端名、上下行字节数与
最近在线时间。同一客户端 ID 从原来源 IP 重新接入会顶掉旧会话并复用同一个端口，
旧会话仍在线时来自其他 IP 的同 ID 登录会被拒绝，空闲连接也必须同源；
端口范围用满时新客户端无法接入，日志会提示调大 `socks_port_count`。

客户端唯一 ID 由程序首次运行时生成并写回 `client.json`，因此删掉该文件会换到新的 SOCKS5 端口。

## 构建和安装

在仓库根目录执行：

```powershell
.\platforms\build-openwrt.ps1
```

默认生成两个角色与两个架构，版本号取自 `common/version`：

- `dist/tunnelexit-<version>-openwrt-x64.ipk`（`Architecture: x86_64`，服务端）
- `dist/tunnelexit-<version>-openwrt-arm64.ipk`（`Architecture: aarch64_generic`，服务端）
- `dist/tunnelexit-client-<version>-openwrt-x64.ipk`（`Architecture: x86_64`，客户端）
- `dist/tunnelexit-client-<version>-openwrt-arm64.ipk`（`Architecture: aarch64_generic`，客户端）

也可用 `-Arch x64` 或 `-Role client` 单独构建。把匹配设备架构的 `.ipk` 复制到 OpenWrt 后执行：

```sh
opkg install /tmp/tunnelexit-1.0.0-openwrt-x64.ipk
/etc/init.d/tunnelexit enable
/etc/init.d/tunnelexit start
```

客户端包的包名与服务名都改成 `tunnelexit-client`：

```sh
opkg install /tmp/tunnelexit-client-1.0.0-openwrt-x64.ipk
/etc/init.d/tunnelexit-client enable
/etc/init.d/tunnelexit-client start
```

LuCI 中访问 **服务 → TunnelExit** 查看服务状态与节点流量表，访问 **服务 → TunnelExit 客户端** 打开客户端配置页；
浏览器直接打开服务端根路径则是不命名任何客户端的公开展示页。
服务是否开机启动由 `/etc/rc.d/` 中的软链接控制，服务端为 `START=95`，客户端为 `START=96`。
两个包可以同装，但不能都把隧道端口设成 9733，客户端连接的服务端不要求是本机。

## 打包要点

- Go 侧 `CGO_ENABLED=0`，产物静态链接，因此 `control` 不声明 `libc` 依赖。
- `.ipk` 是 `ar` 归档，成员顺序必须是 `debian-binary`、`control.tar.gz`、`data.tar.gz`。
- 服务端包带 UCI 配置、LuCI 状态页与归属地库，客户端包只带 procd 服务脚本、跳转页与二进制，因此体积小得多。
- 只有服务端包声明 `conffiles`（`/etc/config/tunnelexit`）；客户端配置在首次运行时生成，不随包分发。
- 文本文件统一写入 LF 无 BOM，避免 CRLF 破坏 shebang 与 UCI 解析。
- 两个归属地库共约 47 MB，路由器 flash 紧张时用 `TUNNELEXIT_SKIP_GEOIP=1` 打包，或用 `TUNNELEXIT_SKIP_GEOIP_V6=1` 只留 IPv4 库；跳过的库对应的地址族会把归属地显示为未知地区。
