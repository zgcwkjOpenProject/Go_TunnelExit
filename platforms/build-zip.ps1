#Requires -Version 7.0
<#
.SYNOPSIS
构建 TunnelExit 的多平台便携 ZIP 包

.DESCRIPTION
从 common/ 按 $ZipTargets 配置交叉编译服务端与客户端两个二进制，连同各自的
默认配置、归属地库与说明文件组装到 build/zip/ 下的单个发布目录，再各压成一个
ZIP 输出到 dist/：一个平台架构一个包，包内两个角色都能直接运行

.EXAMPLE
.\platforms\build-zip.ps1
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# 构建配置：每个平台架构各出一个同时含服务端与客户端的便携包
# 需要单角色安装包的设备取 NAS 套件或 Android 包，便携包不再按角色拆分
$ZipTargets = @(
    @{ Platform = 'Windows'; GOOS = 'windows'; GOARCH = 'amd64'; Ext = '.exe'; OutputSuffix = 'windows-x64' }
    @{ Platform = 'Windows'; GOOS = 'windows'; GOARCH = 'arm64'; Ext = '.exe'; OutputSuffix = 'windows-arm64' }
    @{ Platform = 'Linux'; GOOS = 'linux'; GOARCH = 'amd64'; Ext = ''; OutputSuffix = 'linux-x64' }
    @{ Platform = 'Linux'; GOOS = 'linux'; GOARCH = 'arm64'; Ext = ''; OutputSuffix = 'linux-arm64' }
    @{ Platform = 'Linux'; GOOS = 'linux'; GOARCH = 'arm'; Ext = ''; OutputSuffix = 'linux-armv7' }
    @{ Platform = 'macOS'; GOOS = 'darwin'; GOARCH = 'amd64'; Ext = ''; OutputSuffix = 'darwin-x64' }
    @{ Platform = 'macOS'; GOOS = 'darwin'; GOARCH = 'arm64'; Ext = ''; OutputSuffix = 'darwin-arm64' }
    @{ Platform = 'FreeBSD'; GOOS = 'freebsd'; GOARCH = 'amd64'; Ext = ''; OutputSuffix = 'freebsd-x64' }
)

# 路径与发布目录
$PlatformRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = Split-Path -Parent $PlatformRoot
$Common = Join-Path $Root 'common'
$VersionFile = Join-Path $Common 'version'
$BuildRoot = Join-Path $Root 'build'
$StageRoot = Join-Path $BuildRoot 'zip'
$DistDir = Join-Path $Root 'dist'

. (Join-Path $PlatformRoot 'build-common.ps1')

$Version = Get-TunnelExitVersion -VersionFile $VersionFile
Write-Host "[version] 发布版本: $Version" -ForegroundColor Green

if (-not (Test-Path (Join-Path $Common 'go.mod'))) {
    throw '缺少 common/go.mod'
}

# 压缩暂存目录到 dist/ 并报告产物体积
function New-ZipArchive {
    param(
        [Parameter(Mandatory)][string]$ReleaseDir,
        [Parameter(Mandatory)][string]$ZipPath
    )
    if (Test-Path $ZipPath) { Remove-Item -LiteralPath $ZipPath -Force }
    Compress-Archive -LiteralPath $ReleaseDir -DestinationPath $ZipPath -CompressionLevel Optimal
    if (-not (Test-Path $ZipPath)) { throw "ZIP 未生成：$ZipPath" }
    $size = [Math]::Round((Get-Item $ZipPath).Length / 1MB, 2)
    Write-Host "[zip] 打包完成：$ZipPath ($size MB)" -ForegroundColor Green
}

# 组装并压缩单个便携发布包，包内同时放下服务端与客户端
function New-PortableZip {
    param(
        [Parameter(Mandatory)][string]$Platform,
        [Parameter(Mandatory)][string]$GOOS,
        [Parameter(Mandatory)][string]$GOARCH,
        [AllowEmptyString()][string]$Ext,
        [Parameter(Mandatory)][string]$OutputSuffix
    )
    $releaseName = "tunnelexit-$Version-portable-$OutputSuffix"
    $releaseDir = Join-Path $StageRoot $releaseName

    New-Item -ItemType Directory -Path $releaseDir -Force | Out-Null
    Invoke-TunnelExitBuild -SourceDir $Common -OutPath (Join-Path $releaseDir "tunnelexit-server$Ext") `
        -Package './cmd/server' -GOOS $GOOS -GOARCH $GOARCH `
        -Version $Version -Platform ("zip-server_{0}" -f $OutputSuffix)
    Invoke-TunnelExitBuild -SourceDir $Common -OutPath (Join-Path $releaseDir "tunnelexit-client$Ext") `
        -Package './cmd/client' -GOOS $GOOS -GOARCH $GOARCH `
        -Version $Version -Platform ("zip-client_{0}" -f $OutputSuffix)

    # 两个角色各带一份可直接编辑的默认配置，首次启动即会读取
    Write-TunnelExitServerConfig -Path (Join-Path $releaseDir 'server.json')
    Write-TunnelExitClientConfig -Path (Join-Path $releaseDir 'client.json')
    # 归属地展示只有服务端用得到，库文件放在同一目录即可
    Copy-TunnelExitGeoIP -RepoRoot $Root -DestDir $releaseDir

    $serverRun = if ($Ext -eq '.exe') { '.\tunnelexit-server.exe' } else { 'chmod +x tunnelexit-server && ./tunnelexit-server' }
    $clientRun = if ($Ext -eq '.exe') { '.\tunnelexit-client.exe' } else { 'chmod +x tunnelexit-client && ./tunnelexit-client' }
    $archText = if ($GOARCH -eq 'amd64') { 'x64' } else { $GOARCH }
    Write-TunnelExitReadme -Path (Join-Path $releaseDir 'README.txt') -Title "TunnelExit $Platform $archText" -Lines @(
        '本包同时含服务端与客户端，两个角色都可在本机直接运行；同一台机器同时运行两个角色时'
        '请用 TUNNELEXIT_APP_DIR 分别指定运行目录，否则两者的配置与日志会写到同一个目录里'
        ''
        '服务端（部署在公网服务器或 NAS 上，对外提供 SOCKS5 端口）：'
        "  $serverRun"
        '  首次启动会在运行目录的 keys 子目录生成 private_key.pem、server.crt 与 public_key.pem，打开 public_key.pem 复制整份公钥填到客户端配置页'
        '  默认在隧道端口 9733 上同时接受客户端登录与预建的空闲连接，隧道端口只接受 TLS 连接，公钥不对的客户端连不上'
        '  SOCKS5 端口在 19733 起的 2500 个端口内按客户端 ID 与来源 IP 分配'
        '  同一客户端 ID 只接受原来源 IP 重连，旧会话仍在线时来自其他地址的同 ID 登录会被拒绝，空闲数据连接也必须与该地址一致'
        '  节点展示页默认只监听 127.0.0.1:9732，浏览器打开 http://127.0.0.1:9732/ 可看节点状态与归属地'
        '  展示页按状态分组排序，在线与建立中的节点在最上面，同组内按 SOCKS5 端口升序'
        '  日志写在运行目录的 logs 子目录下：tunnelexit.log 为程序日志并按天归档，start.log 为启动脚本日志'
        '  节点记录默认长期保留，把 server.json 的 nodeKeepDays 设为 7 可自动清理超过 7 天未在线的记录'
        '  展示页不含来源 IP 与客户端名称，服务端也没有管理入口，调整参数只能改 server.json 或环境变量'
        '  升级或换机时请把 keys 子目录一并搬走，重新生成密钥会让已下发的公钥全部失效'
        ''
        '客户端（部署在需要借用其网络出口的机器上）：'
        "  $clientRun"
        '  默认连接 127.0.0.1:9733，请编辑 client.json 的 serverHost 与服务端公钥 serverKey 指向服务端'
        '  服务端公钥是多行 PEM 文本，JSON 里换行写成 \n；未填写或填错时客户端不会连接，每小时自动重试一次，改好配置页后立即重连'
        '  客户端唯一 ID 首次运行自动生成并写入 client.json，删除后重新生成会拿到新的 SOCKS5 端口'
        '  配置管理页默认只监听 127.0.0.1:7003，浏览器打开 http://127.0.0.1:7003/ 可改配置与看日志'
        '  需要从其他机器打开时把 client.json 的 webHost 改成 0.0.0.0，并用 TUNNELEXIT_WEB_TOKEN 设置访问口令'
        ''
        '界面挂载路径：'
        '  server.json 与 client.json 都有 webPath 字段，设成 /tunnelexit 后页面地址变成'
        '  http://127.0.0.1:9732/tunnelexit/ （客户端为 7003），接口地址随页面一并变化，便于反向代理走子路径'
        '  访问不带结尾斜杠的前缀会跳转到带斜杠的地址'
        ''
        '常用环境变量：'
        '  TUNNELEXIT_APP_DIR            指定运行目录（配置与日志的存放位置），同机双角色时必设'
        '  TUNNELEXIT_PORT               覆盖隧道端口，两个角色共用同名变量'
        '  TUNNELEXIT_SOCKS_HOST         覆盖服务端 SOCKS5 监听地址'
        '  TUNNELEXIT_SOCKS_PORT_BASE    覆盖服务端 SOCKS5 分配起始端口'
        '  TUNNELEXIT_SOCKS_PORT_COUNT   覆盖服务端 SOCKS5 端口个数，用满后拒绝新客户端'
        '  TUNNELEXIT_GEOIP_FILE         覆盖服务端归属地库路径，指向不存在的文件即关闭归属地展示'
        '  TUNNELEXIT_GEOIP_FILE_V6      覆盖服务端 IPv6 归属地库路径，指向不存在的文件即关闭 IPv6 归属地展示'
        '  TUNNELEXIT_POOL_SIZE          覆盖服务端空闲连接池大小'
        '  TUNNELEXIT_NODE_KEEP_DAYS      覆盖服务端节点记录保留天数，零表示不删除'
        '  TUNNELEXIT_SERVER             覆盖客户端连接的服务端地址'
        '  TUNNELEXIT_SERVER_KEY         覆盖客户端配置的服务端公钥 PEM 文本，两个角色共用同名变量'
        '  TUNNELEXIT_CLIENT_ID          覆盖客户端唯一 ID，一般不必设置'
        '  TUNNELEXIT_CLIENT_NAME        覆盖客户端名称，仅用于服务端日志与节点记录，展示页不显示'
        '  TUNNELEXIT_CLIENT_POOL_SIZE   覆盖客户端空闲连接池大小'
        '  TUNNELEXIT_WEB_HOST           覆盖界面监听地址，两个角色共用同名变量'
        '  TUNNELEXIT_WEB_PORT           覆盖界面监听端口，两个角色共用同名变量'
        '  TUNNELEXIT_WEB_PATH           覆盖界面挂载路径前缀，经反向代理走子路径时使用'
        '  TUNNELEXIT_WEB_TOKEN          设置客户端配置管理页的口令，不设置则不做校验'
        '  TUNNELEXIT_LOG_KEEP_DAYS      覆盖日志保留天数，设为零则不写日志文件'
        ''
        '外部程序把 SOCKS5 代理指向服务端地址与日志里给出的端口，出网流量就会由客户端所在机器完成'
    )

    New-ZipArchive -ReleaseDir $releaseDir -ZipPath (Join-Path $DistDir ($releaseName + '.zip'))
}

Remove-TunnelExitStage -Path $StageRoot -BuildRoot $BuildRoot
New-Item -ItemType Directory -Path $DistDir -Force | Out-Null

for ($index = 0; $index -lt $ZipTargets.Count; $index++) {
    $target = $ZipTargets[$index]
    Write-Host "[$($index + 1)/$($ZipTargets.Count)] 构建 $($target.OutputSuffix) 的便携包 ..." -ForegroundColor Cyan
    New-PortableZip -Platform $target.Platform -GOOS $target.GOOS -GOARCH $target.GOARCH `
        -Ext $target.Ext -OutputSuffix $target.OutputSuffix
}

Write-Host "`n构建完成，版本 $Version 的 $($ZipTargets.Count) 个 ZIP（每个平台架构一个包，同时含服务端与客户端）已输出到 dist/" -ForegroundColor Green
