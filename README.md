# TunnelExit

把服务器的 SOCKS5 出网流量交回客户端所在网络执行的反向隧道方案

```
[外部客户端] --SOCKS5--> [服务端] ==隧道==> [客户端] --dial--> [目标资源]
```

本地或内网机器上运行客户端，服务器或 NAS 上运行服务端。外部程序连服务端暴露的 SOCKS5 端口，实际拨号访问目标资源的是客户端所在的机器，因此服务器可以用客户端的 IP 访问数据。适合让公网服务器借用内网出口、让 NAS 借用本地网络出口等场景

只用 Go 标准库实现，没有第三方依赖。IP 归属地展示用随包分发的离线库 `ip2region_v4.xdb` 与 `ip2region_v6.xdb`（Apache-2.0），分别覆盖 IPv4 与 IPv6 来源，服务端不访问任何外部接口

## 交付形态

| 平台 | 产物 | 角色 | 说明 |
| --- | --- | --- | --- |
| 飞牛 fnOS | `.fpk` | 服务端 / 客户端 | arm64 与 x86_64，两个套件分别安装 |
| 群晖 DSM 7 | `.spk` | 服务端 / 客户端 | armv8 与 x86_64，套件中心安装 |
| OpenWrt | `.ipk` | 服务端 / 客户端 | x64 与 arm64，服务端包带 LuCI 状态页，客户端包带配置页入口 |
| Android | `.apk` | 客户端 | arm64-v8a，内置客户端与前台服务 |
| 通用 | `.zip` | 服务端 / 客户端 | Windows、Linux、macOS、FreeBSD 便携包，一个包同时含两个角色与各自的配置，按架构区分 |

NAS 套件一个包只装一个角色：服务端包以 `tunnelexit-` 开头，客户端包以 `tunnelexit-client-` 开头，Android 只出客户端。便携包则同时放两个角色，拿到某个架构的机器取对应的那一个 ZIP 即可

套件在应用中心与桌面上的显示名统一是 TunnelExit，角色由套件说明区分。NAS 套件与 OpenWrt 包都把配置与日志放在卸载后仍然保留的目录里，安装、升级后自动还原，卸载向导默认保留配置，只有选择删除数据时才清理，重装后不用重新填一遍参数

桌面入口：飞牛的服务端与客户端套件都在系统桌面留一个图标，服务端图标点开是只读的节点展示页，客户端图标点开是配置页，两个页面各自只监听套件目录内的 unix socket，由 fnOS 网关按 `/app/tunnelexit` 与 `/app/tunnelexit-client` 两个前缀分别代理过去；群晖客户端套件同样在桌面留一个图标，套件装好的 nginx 段把 `/webman/zgcwkj/tunnelexit-client/` 回源到 `127.0.0.1:7003`，配置页仍只监听本机；OpenWrt 客户端包在 LuCI 的「服务」下加一个菜单项，页面直接跳到路由器地址的 7003 端口，因此要把客户端的 `webHost` 改成 `0.0.0.0` 并设好访问口令。群晖与 OpenWrt 的服务端包不提供桌面图标与管理入口

含服务端的包都附带离线归属地库 `ip2region_v4.xdb` 与 `ip2region_v6.xdb`（共约 47 MB），flash 紧张的设备可用 `TUNNELEXIT_SKIP_GEOIP=1` 在打包时全部跳过，或用 `TUNNELEXIT_SKIP_GEOIP_V6=1` 只留 IPv4 库；Android 客户端不展示归属地，包里不带库文件

## 快速开始

服务端在 NAS 套件的服务端包里，也在便携包里；客户端在 NAS 套件的客户端包、Android 包和便携包里。便携包含两个角色的可执行文件，可用 `TUNNELEXIT_APP_DIR` 指定各自的运行目录。拿到可执行文件直接运行即可

```bash
# 服务器：启动服务端，监听隧道端口，并在端口范围内提供 SOCKS5 端口
./tunnelexit-server

# 本地：启动客户端，主动连出并在本机执行出网
./tunnelexit-client
```

客户端首次运行会在运行目录生成 `client.json`，把 `serverHost` 改成服务器的地址、把 `serverKey` 填成服务端 `keys/public_key.pem` 里的公钥即可；同时会自动生成客户端唯一 ID 并写回同一文件

服务端首次运行会在运行目录生成 `server.json` 与 `keys/` 子目录（`private_key.pem` 私钥、`server.crt` 自签名证书、`public_key.pem` 公钥），并按客户端 ID 与来源 IP 在 19733 起的端口范围内分配一个固定的 SOCKS5 端口，日志里会输出端口号。外部程序把 SOCKS5 代理指向 `服务器地址:该端口`，出网流量就会由客户端所在机器完成。`keys/` 必须随升级一并保留，重新生成会让客户端已填的公钥全部失效

服务端与客户端都会把启动参数、客户端接入与出网过程写进运行目录下 `logs/tunnelexit.log`，平台启动脚本自身的日志是同一目录下的 `start.log`；直接在前台运行时，同样的日志会同步打印到终端。程序日志按天归档成 `logs/tunnelexit-日期.log`，早于保留天数（默认 3 天）的归档在启动与跨天时删除，因此日志不会无限增长

在浏览器里打开服务端根路径就能看到节点状态页；改配置在客户端本机的配置页里做，详见「展示页与配置管理页」一节

## 配置

配置只有一个权威来源：程序先读运行目录下的 `server.json` 或 `client.json`，再用环境变量覆盖。平台套件不生成配置模板，直接通过环境变量或界面注入

### 服务端

| 字段 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `port` | `TUNNELEXIT_PORT` | 9733 | 隧道端口，客户端登录与预建的空闲连接共用 |
| `socksHost` | `TUNNELEXIT_SOCKS_HOST` | 0.0.0.0 | SOCKS5 监听地址 |
| `socksPortBase` | `TUNNELEXIT_SOCKS_PORT_BASE` | 19733 | 分配端口范围的起点 |
| `socksPortCount` | `TUNNELEXIT_SOCKS_PORT_COUNT` | 2500 | 端口范围的数量，超出范围的新客户端拒绝接入 |
| `poolSize` | `TUNNELEXIT_POOL_SIZE` | 16 | 每个客户端的空闲连接数 |
| `nodeKeepDays` | `TUNNELEXIT_NODE_KEEP_DAYS` | 0 | 节点记录保留天数，超过该天数未在线的记录在启动与运行中自动清理，`0` 表示不删除 |
| `webHost` | `TUNNELEXIT_WEB_HOST` | 127.0.0.1 | 节点展示页的监听地址 |
| `webPort` | `TUNNELEXIT_WEB_PORT` | 9732 | 节点展示页的监听端口 |
| `webPath` | `TUNNELEXIT_WEB_PATH` | / | 展示页的挂载路径前缀，经反向代理走子路径时填写 |

归属地展示使用随包分发的离线库 `ip2region_v4.xdb`（IPv4 来源）与 `ip2region_v6.xdb`（IPv6 来源），环境变量 `TUNNELEXIT_GEOIP_FILE` 与 `TUNNELEXIT_GEOIP_FILE_V6` 可分别指向其他位置；指向不存在的文件等于关闭该地址族的归属地展示，服务端只记一行日志。来源地址属于本机、内网或链路本地范围时直接显示为局域网，不查库

节点展示页按状态分组排序，在线与建立中的节点排在最上面，同组内按 SOCKS5 端口升序。在线会话每轮心跳都会刷新记录的最近在线时间，因此 `nodeKeepDays` 为正值时只会清掉长期没有客户端接入的记录，被清掉的客户端下次登录会重新分配端口

隧道端口只接受 TLS 连接：服务端首次启动会在运行目录的 `keys/` 子目录生成 RSA 私钥 `private_key.pem`、自签名证书 `server.crt` 与公钥 `public_key.pem`，客户端必须填入 `public_key.pem` 里的同一份公钥才能接入；未填写或填错时客户端保持进程运行并每小时自动重试一次，在配置页改好后立即重连。非法或未加密的连接在握手段即被丢弃

### 客户端

| 字段 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `serverHost` | `TUNNELEXIT_SERVER` | 127.0.0.1 | 服务端地址 |
| `port` | `TUNNELEXIT_PORT` | 9733 | 服务端隧道端口，与服务端一致 |
| `serverKey` | `TUNNELEXIT_SERVER_KEY` | 空 | 服务端公钥 PEM 文本，取自服务端 `keys/public_key.pem`，客户端据此校验对端身份，未填写或填错一律无法连接 |
| `id` | `TUNNELEXIT_CLIENT_ID` | 首次运行自动生成 | 客户端唯一 ID，与来源 IP 一起决定服务端分配哪个 SOCKS5 端口 |
| `name` | `TUNNELEXIT_CLIENT_NAME` | 主机名 | 客户端名称，仅写入服务端日志与节点记录，展示页不显示 |
| `poolSize` | `TUNNELEXIT_CLIENT_POOL_SIZE` | 16 | 空闲连接数 |
| `webHost` | `TUNNELEXIT_WEB_HOST` | 127.0.0.1 | 配置管理页的监听地址 |
| `webPort` | `TUNNELEXIT_WEB_PORT` | 7003 | 配置管理页的监听端口 |
| `webPath` | `TUNNELEXIT_WEB_PATH` | / | 配置管理页的挂载路径前缀，经反向代理走子路径时填写 |

通用环境变量 `TUNNELEXIT_APP_DIR` 可指定运行目录，配置、`logs/` 日志目录与 `keys/` 密钥目录都放在该目录；`TUNNELEXIT_LOG_KEEP_DAYS` 可改日志保留天数，设为 `0` 则不写日志文件，日志只走标准输出

### 展示页与配置管理页

启动后浏览器直接打开根路径，服务端看到的是节点展示页，客户端看到的是配置管理页，不用再手改 JSON

| 程序 | 路径 | 可做的事 |
| --- | --- | --- |
| 服务端 | `http://127.0.0.1:9732/` | 只读的节点展示页：节点状态、归属地、端口与流量统计，不含 IP 与客户端名 |
| 客户端 | `http://127.0.0.1:7003/` | 配置管理页：修改服务端地址、隧道端口与服务端公钥、查看运行日志 |

服务端页面可以安全地给外部看：它只包含归属地与流量统计，不含来源 IP 与客户端名称。服务端没有管理入口也没有配置接口，调整参数请改 `server.json` 或环境变量

客户端页面是管理页，可以修改配置并查看日志，它与 `/api/config`、`/api/log` 一起受 `TUNNELEXIT_WEB_TOKEN` 保护，不设口令时不做校验

两边的界面都能挂在子路径下：把 `webPath` 设成 `/tunnelexit` 之类的值后页面地址变成 `http://127.0.0.1:9732/tunnelexit/`，访问不带结尾斜杠的前缀会跳转到带斜杠的地址，页面内的接口请求也自动带上该前缀

界面默认只监听本机。需要从其他机器打开时把 `webHost` 改成 `0.0.0.0`；服务端页面不含敏感信息，客户端页面则必须用 `TUNNELEXIT_WEB_TOKEN` 设置访问口令，打开时带上 `?token=口令`，浏览器会自动记住

平台外壳用 `TUNNELEXIT_WEB_SOCKET` 把界面挂到套件目录内的 unix socket 时，界面只监听该 socket，`webHost` 与 `webPort` 不再生效，访问入口由 `TUNNELEXIT_WEB_PATH` 指定的前缀决定，飞牛两个套件就是这样把展示页与配置页交给系统网关的

客户端配置页里保存的配置写入 `client.json` 并**立即生效**，不需要重启进程：入口会按新参数重建隧道连接。页面只提供影响隧道的参数，配置界面自身的监听地址、端口与路径前缀不在此页修改，只能直接编辑 `client.json` 或用 `TUNNELEXIT_WEB_HOST`、`TUNNELEXIT_WEB_PORT`、`TUNNELEXIT_WEB_PATH` 环境变量指定后重启进程。界面监听失败只在日志里记一笔，不影响隧道本身

## 工作原理

1. 客户端用配置里的公钥校验服务端证书后连上隧道端口并发送登录请求，登录帧与全部数据帧都在加密通道里，公钥不匹配的客户端在 TLS 握手段就被丢弃
2. 服务端登记客户端、按客户端 ID 与来源 IP 分配或复用它的 SOCKS5 端口，并回告端口与约定连接数
3. 服务端下发补池请求，客户端按数量预建数据连接，服务端按首帧类型把它归入空闲连接池
4. 外部程序连上 SOCKS5 端口并发出 CONNECT 请求
5. 服务端从池中取出一条空闲连接，在该连接上下发建流指令
6. 客户端在本机拨号访问目标，把结果回告服务端
7. 成功则由服务端应答 SOCKS5 成功并双向转发；失败则按原因映射为对应应答码

客户端首次运行生成一个唯一 ID 并保存在 `client.json` 里，服务端用「该 ID + 登录来源 IP」这对组合算出一个起始偏移，再在 `socksPortBase` 起的 `socksPortCount` 个端口里顺序找一个空闲端口分配，之后一直复用；端口连同上下行流量统计一起落盘到 `clients.json`，重启后端口不变；范围用满时新客户端无法接入，日志会提示调大 `socksPortCount`

同一个客户端 ID 从原来源 IP 重新接入会顶掉旧会话并复用同一个端口，因此删除客户端 ID 会换到新端口；不同 ID 的客户端互不影响

ID 与来源 IP 缺一不可：旧会话仍在线时，来自其他 IP 的同 ID 登录会被拒绝，把 `client.json` 复制到别的机器既顶不掉正在运行的真客户端，也进不了它的空闲连接池；真客户端换了网络，等旧会话断开后重连即可自动接管并更新记录里的来源 IP。注意同一出口 IP 后的机器彼此无法区分，这条规则只能挡住跨 IP 冒用

## 从源码构建

需要 PowerShell 7 与 Go。构建各平台包还需要对应工具链：Android 需要 JDK 21、Android SDK、NDK 与 build-tools，飞牛套件需要 `fnpack.exe`（脚本会自动下载）

```powershell
# 交互选择平台
./build.ps1

# 指定平台
./build.ps1 -Only zip,fnos,synology,openwrt,android
```

产物统一输出到 `dist/`，构建暂存位于 `build/`。版本号取自 `common/version`

也可以只编译本机程序：

```bash
cd common
go build -o tunnelexit-server ./cmd/server
go build -o tunnelexit-client ./cmd/client
```

## 测试

```bash
cd common
go vet ./...
go test ./... -timeout 300s
```

`common/test/` 提供三层验证：协议帧与消息编解码、SOCKS5 握手与应答码映射、按 IP 分配端口与流量统计的持久化，以及通过公开 API 驱动服务端与客户端的完整端到端链路，另外覆盖界面的配置文件读写、口令校验与日志接口

## 开发规范

开发前先阅读 `AGENTS.md`，其中包含目录职责、架构约定、注释规范与验证清单
