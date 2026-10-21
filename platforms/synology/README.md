# TunnelExit 群晖套件外壳

本目录只放群晖 Synology DSM 7 的 `.spk` 套件外壳，按角色分 `server/` 与 `client/` 两套。
业务代码在 `common/`：`platforms/build-synology.ps1` 按角色编译 `tunnelexit-server` 或 `tunnelexit-client`，
再把对应的套件外壳一起打进 `.spk`。

服务端套件让 NAS 充当地道入口，客户端套件让 NAS 借用别的机器的网络出口，两者可以同装也可以只装一个，
包内没有切换角色的入口。

## 目录结构

```text
platforms/synology/
├── README.md
├── PACKAGE_ICON.PNG            套件图标（512） 构建时复制到 .spk 根
├── PACKAGE_ICON_256.PNG        套件图标（256） 构建时复制到 .spk 根
├── server/                     服务端套件外壳
│   ├── INFO                    套件元数据：包名、版本、架构
│   ├── scripts/                套件生命周期脚本
│   │   ├── functions           数据搬运与清理的公共函数
│   │   ├── preinst / postinst  安装前后
│   │   ├── preuninst / postuninst  卸载前后
│   │   ├── preupgrade / postupgrade 升级前后
│   │   └── start-stop-status   套件中心启停入口
│   ├── conf/
│   │   ├── privilege           以普通套件用户运行
│   │   ├── resource            声明 systemd 用户服务
│   │   └── systemd/            pkguser-tunnelexit.service
│   ├── WIZARD_UIFILES/         安装与卸载向导文案
│   └── package/app/
│       └── start.sh            systemd ExecStart，投递用户数据后拉起服务端
└── client/                     客户端套件外壳，比服务端多 DSM 桌面入口
    ├── INFO
    ├── scripts/
    ├── conf/systemd/           pkguser-tunnelexit-client.service
    ├── WIZARD_UIFILES/
    ├── package/app/
    │   └── start.sh            systemd ExecStart，投递用户数据后拉起客户端
    ├── package/ui/             DSM 桌面图标与窗口定义，装到 target/ui/
    └── package/etc/www/        Webman 反代片段，装到 target/etc/www/
```

两个角色共用平台根目录的图标，构建时分别注入各自的二进制与配置文件；
服务端套件额外注入归属地库 `ip2region_v4.xdb` 与 `ip2region_v6.xdb`，客户端套件不带库文件。

## DSM 桌面入口

客户端套件带一个 DSM 桌面图标，点开即打开本机配置管理页，网络不通的 NAS 不需要额外开放端口：

- `package/ui/config` 声明 `SYNO.SDS.TunnelExit` 应用与窗口，图标取 `ui/images/icon_<尺寸>.png`，
  展示版本由构建脚本按 `common/version` 注入
- `package/ui/Main.js` 先取当前 DSM 会话的 `SynoToken`，再以 iframe 打开配置页
- `package/etc/www/tunnelexit-client.nginx.conf` 由 `conf/resource` 的 `web-config` 安装，把
  `/webman/zgcwkj/tunnelexit-client/` 回源到 `127.0.0.1:7003`，因此 `start.sh` 必须同时把
  `TUNNELEXIT_WEB_PATH` 设成同一前缀

图标由 `platforms/make-icons.ps1` 生成，DSM 只认 png，不能换成 webp；服务端套件没有界面，不带 `ui/` 目录。

## 与飞牛文件对应关系

| 飞牛 | 群晖 | 说明 |
|---|---|---|
| `platforms/fnos/<角色>/manifest` | `INFO` | 元信息：包名、版本、架构 |
| `platforms/fnos/<角色>/cmd/main` | `package/app/start.sh` + `scripts/start-stop-status` | 启停逻辑 |
| `platforms/fnos/config/privilege` | `conf/privilege` | 都是 `run-as=package`，套件用户各自独立 |
| `platforms/fnos/config/resource` | `conf/resource` | 群晖额外声明 systemd 用户服务 |
| `platforms/fnos/<角色>/wizard/install` | `WIZARD_UIFILES/install_uifile` | 安装向导 |
| `platforms/fnos/<角色>/wizard/uninstall` | `WIZARD_UIFILES/uninstall_uifile` | 卸载向导 |
| `platforms/fnos/ICON*.PNG` | `PACKAGE_ICON*.PNG` | 套件图标 |

## 用户数据和生命周期

群晖安装包会把程序放在 `target/app/`，把运行时数据保存在套件用户自己的 `var/app/`。
启动时把需要保留的文件移动到程序目录，停止时再搬运回去；源文件不存在时跳过。

服务端套件 `/var/packages/tunnelexit/var/app/`：

- `server.json`（服务端监听参数，可手工修改后重启套件生效）
- `clients.json`（客户端唯一 ID 到来源 IP、SOCKS5 端口、流量与最近在线时间的记录，超过 `server.json` 的 `nodeKeepDays` 天未在线的记录会被自动清理）
- `keys/`（服务端首次启动生成的 `private_key.pem` 私钥、`server.crt` 自签名证书与 `public_key.pem` 公钥，必须随升级保留，否则已下发的公钥全部失效）
- `logs/`（日志目录，程序日志 `tunnelexit.log` 按天归档成 `tunnelexit-日期.log` 并默认保留 3 天，启动脚本日志为 `start.log`）

客户端套件 `/var/packages/tunnelexit-client/var/app/`：

- `client.json`（服务端地址、隧道端口、服务端公钥、客户端唯一 ID 与配置页参数）
- `logs/`（日志目录，程序日志 `tunnelexit.log` 按天归档成 `tunnelexit-日期.log` 并默认保留 3 天，启动脚本日志为 `start.log`）

因此升级替换 `target/` 不会丢端口与流量统计。
卸载向导默认保留用户数据，选择「删除套件所有数据」时才清理
`/var/packages/<包名>/var/app/`。

同一客户端 ID 从原来源 IP 重新接入会顶掉旧会话并复用同一个 SOCKS5 端口，端口在 `socksPortBase` 起的
`socksPortCount` 个端口里按客户端 ID 与来源 IP 分配，范围用满时新客户端无法接入；
旧会话仍在线时来自其他 IP 的同 ID 登录会被拒绝，真客户端换网络后重连即可接管。

## 构建

在仓库根目录执行：

```powershell
.\platforms\build-synology.ps1
```

脚本用 Go 交叉编译 `common/`（`GOOS=linux CGO_ENABLED=0`），再用 .NET `System.Formats.Tar`
生成 `package.tgz` 并显式写入 Unix 权限位（二进制与脚本 `0755`，普通文件 `0644`），文本统一转
LF 行尾。默认同时构建两个角色与两个架构，版本号从 `common/version` 读取并注入暂存的 `INFO`：

- `dist/tunnelexit-<version>-synology-x86_64.spk` 与 `dist/tunnelexit-<version>-synology-armv8.spk`（服务端）
- `dist/tunnelexit-client-<version>-synology-x86_64.spk` 与 `dist/tunnelexit-client-<version>-synology-armv8.spk`（客户端）

也可以用 `-Role server` 或 `-Role client` 只构建其中一个角色。

## 安装与排查

```bash
sudo synopkg install /path/to/tunnelexit-1.0.0-synology-x86_64.spk
sudo synopkg status tunnelexit
journalctl -u pkguser-tunnelexit -n 80
```

客户端套件把包名换成 `tunnelexit-client` 即可，服务名为 `pkguser-tunnelexit-client`：

```bash
sudo synopkg install /path/to/tunnelexit-client-1.0.0-synology-x86_64.spk
sudo synopkg status tunnelexit-client
journalctl -u pkguser-tunnelexit-client -n 80
```

服务端默认监听：隧道端口 9733、SOCKS5 端口从 19733 起分配。隧道端口只接受 TLS 连接，
首次启动会在套件目录的 `keys/` 里生成 `private_key.pem`、`server.crt` 与 `public_key.pem`，
把 `public_key.pem` 的整份内容填进客户端即可，未填写或填错都会连不上。若与 NAS 上已有服务冲突，
修改 `server.json` 中的 `port`、`socksPortBase`、`socksPortCount` 后重启套件即可。

客户端安装后需要把 `client.json` 的 `serverHost` 改成服务端地址并把 `serverKey` 填成服务端 `keys/public_key.pem` 里的公钥（多行文本，JSON 里换行写成 `\n`），既能直接改文件，也可以点击 DSM 桌面上的
TunnelExit 图标打开配置管理页修改，在配置页里保存后立即生效、无需重启套件（直接改文件仍需重启套件）；客户端唯一 ID 由程序首次运行时生成并写回同一文件。
桌面图标走 DSM 登录态与 Webman 反代，不需要额外开放端口；要绕过 Webman 直连 `http://NAS地址:7003/` 时，
先设置 `TUNNELEXIT_WEB_TOKEN` 口令。

## 维护注意

- 发布版本只修改根目录 `common/version`；`INFO` 与 `platforms/fnos/<角色>/manifest` 的版本保持 `0.0.0`。
- 业务改动只改 `common/` 的 Go 源码，重新运行 `platforms/build-synology.ps1` 即可。
- 新增角色相关的元数据与脚本时要同时改 `server/` 与 `client/`，两套外壳的字段与函数结构保持一致。
