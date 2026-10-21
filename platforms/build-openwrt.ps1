#Requires -Version 7.0
<#
.SYNOPSIS
    在 Windows 上打包 TunnelExit 的 OpenWrt 软件包 .ipk

.DESCRIPTION
    服务端包交叉编译 common/ 生成 tunnelexit-server，客户端包生成 tunnelexit-client，
    再把 platforms/openwrt/ 下对应角色的 procd 服务脚本、包内配置、LuCI 页面
    与编译产物组装到 build/openwrt/ 暂存目录。服务端包带 UCI 配置、LuCI 状态页，
    客户端包只带 procd 服务脚本与跳转到自身配置页的 LuCI 入口

    .ipk 是 ar 归档，依次包含 debian-binary、control.tar.gz、data.tar.gz。
    所有文本文件统一转成 LF，脚本显式写入 755 权限，避免 Windows 打包的常见坑。
    产物按角色输出到 dist/：tunnelexit-* 是服务端包，tunnelexit-client-* 是客户端包

    一个包只装一个角色：路由器充当地道入口时装服务端包，需要让路由器借用别的机器
    的网络出口时装客户端包，机器缺哪个角色就取同版本的另一个包

    版本号从 common/version 读取，无需也无法通过参数覆盖

.PARAMETER Arch
    目标架构，默认生成全部架构

.PARAMETER Role
    包角色，默认生成服务端与客户端两个包

.PARAMETER SkipVerify
    跳过产物内容打印

.EXAMPLE
    .\platforms\build-openwrt.ps1
    .\platforms\build-openwrt.ps1 -Arch x64
    .\platforms\build-openwrt.ps1 -Role client
#>
[CmdletBinding()]
param(
    [ValidateSet('all', 'x64', 'arm64')]
    [string]$Arch = 'all',
    [ValidateSet('all', 'server', 'client')]
    [string]$Role = 'all',
    [switch]$SkipVerify
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# 构建配置
$GoOS = 'linux'
$PackageFlavor = 'openwrt'

# 仓库与构建路径
$PlatformRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = Split-Path -Parent $PlatformRoot
$OpenWrtDir = Join-Path $PlatformRoot 'openwrt'
$Common = Join-Path $Root 'common'
$DistDir = Join-Path $Root 'dist'
$BuildRoot = Join-Path $Root 'build'
$VersionFile = Join-Path $Common 'version'
$BuildCommonScript = Join-Path $PlatformRoot 'build-common.ps1'
$GoModuleFile = Join-Path $Common 'go.mod'

# 架构与角色都取默认值时逐个组合，其余调用复用同一套打包流程
if ($Arch -eq 'all' -or $Role -eq 'all') {
    $archTargets = if ($Arch -eq 'all') { @('x64', 'arm64') } else { @($Arch) }
    $roleTargets = if ($Role -eq 'all') { @('server', 'client') } else { @($Role) }
    foreach ($targetArch in $archTargets) {
        foreach ($targetRole in $roleTargets) {
            if ($SkipVerify) {
                & $PSCommandPath -Arch $targetArch -Role $targetRole -SkipVerify
            }
            else {
                & $PSCommandPath -Arch $targetArch -Role $targetRole
            }
            if ($LASTEXITCODE -ne 0) { throw "$targetArch $targetRole 打包失败，退出码 $LASTEXITCODE" }
        }
    }
    exit 0
}

$archConfig = @{
    x64   = @{ GoArch = 'amd64'; PackageArch = 'x86_64' }
    arm64 = @{ GoArch = 'arm64'; PackageArch = 'aarch64_generic' }
}[$Arch]
$GoArch = $archConfig.GoArch
$PackageArch = $archConfig.PackageArch

# 角色差异：Go 入口、二进制名、包名、包内安装目录与是否附带归属地库
# UCI 配置与 rpcd 授权只服务服务端，客户端配置在运行时生成，两者同一开关控制
$roleConfig = @{
    server = @{
        GoCmd        = './cmd/server'
        Binary       = 'tunnelexit-server'
        PackageName  = 'tunnelexit'
        AssetDir     = 'usr/share/tunnelexit'
        DataDir      = 'etc/tunnelexit'
        InitScript   = 'tunnelexit'
        SourceDir    = 'server'
        HasUciConfig = $true
        IncludeGeoIP = $true
        Description  = '反向 SOCKS5 隧道服务端'
        DescriptionTail = @(
            '客户端连上来后，本机为它分配一个专属 SOCKS5 端口，',
            '外部即可经由该客户端的网络访问内部资源。'
        )
    }
    client = @{
        GoCmd        = './cmd/client'
        Binary       = 'tunnelexit-client'
        PackageName  = 'tunnelexit-client'
        AssetDir     = 'usr/share/tunnelexit-client'
        DataDir      = 'etc/tunnelexit-client'
        InitScript   = 'tunnelexit-client'
        SourceDir    = 'client'
        HasUciConfig = $false
        IncludeGeoIP = $false
        Description  = '反向 SOCKS5 隧道客户端'
        DescriptionTail = @(
            '主动连出服务端后，本机借用自身网络出口为服务端出网，',
            '服务端的 SOCKS5 端口即可访问本机所在网络的资源。'
        )
    }
}[$Role]
$PackageName = $roleConfig.PackageName
# 安装目录与包内相对路径（正斜杠，与 .ipk 内的路径一致）
$AssetDir = $roleConfig.AssetDir
$DataDir = $roleConfig.DataDir

# 包内文件：源路径相对 platforms/openwrt/<角色>/files，目标路径与源路径一致
# LuCI 菜单与页面按包名命名，两个角色都有入口：服务端是状态页，客户端是跳转页
# 两个角色的页面都要读设备上的记录或配置文件，因此都带一份 rpcd 读权限声明
$assetFiles = @(
    ('etc/init.d/' + $roleConfig.InitScript),
    ($AssetDir + '/run.sh'),
    ('usr/share/luci/menu.d/' + $PackageName + '.json'),
    ('www/luci-static/resources/view/' + $PackageName + '.js'),
    ('usr/share/rpcd/acl.d/luci-app-' + $PackageName + '.json')
)
if ($roleConfig.HasUciConfig) {
    $assetFiles += @(
        ('etc/config/' + $roleConfig.InitScript)
    )
}

# 两个角色各用独立暂存目录，避免相互覆盖
$WorkDir = Join-Path $BuildRoot ('openwrt/' + $Role)
$DataRoot = Join-Path $WorkDir 'data'
$ControlRoot = Join-Path $WorkDir 'control'
$FilesSource = Join-Path (Join-Path $OpenWrtDir $roleConfig.SourceDir) 'files'
$BinaryRelPath = ($AssetDir + '/' + $roleConfig.Binary)

# 前置检查
try { Add-Type -AssemblyName System.Formats.Tar -ErrorAction Stop }
catch { throw '需要 PowerShell 7+（依赖 System.Formats.Tar）。请用 pwsh 运行本脚本。' }

if (-not (Test-Path $GoModuleFile)) {
    throw '缺少 common/go.mod'
}
foreach ($p in @($FilesSource)) {
    if (-not (Test-Path $p)) { throw "缺少必需文件或目录: $p" }
}
foreach ($rel in $assetFiles) {
    $src = Join-Path $FilesSource ($rel -replace '/', '\')
    if (-not (Test-Path $src)) { throw "缺少必需文件: $src" }
}

. $BuildCommonScript

$Version = Get-TunnelExitVersion -VersionFile $VersionFile

Remove-TunnelExitStage -Path $WorkDir -BuildRoot $BuildRoot
New-Item -ItemType Directory -Path $DistDir -Force | Out-Null

Write-Host "[1/4] 编译 $($roleConfig.Binary) 并组装文件树 ..." -ForegroundColor Cyan

New-Item -ItemType Directory -Path $ControlRoot -Force | Out-Null

# 包内文本资源直接按源相对路径安装，先建目录再逐个复制
foreach ($rel in $assetFiles) {
    $src = Join-Path $FilesSource ($rel -replace '/', '\')
    $dest = Join-Path $DataRoot ($rel -replace '/', '\')
    New-Item -ItemType Directory -Path (Split-Path -Parent $dest) -Force | Out-Null
    Copy-Item $src $dest
}

Invoke-TunnelExitBuild -SourceDir $Common -OutPath (Join-Path $DataRoot ($BinaryRelPath -replace '/', '\')) `
    -Package $roleConfig.GoCmd -GOOS $GoOS -GOARCH $GoArch `
    -Version $Version -Platform ("{0}_{1}" -f $PackageFlavor, $PackageArch)

# 归属地展示只有服务端用得到，库文件只随服务端包分发
# 两个库共约 47 MB，路由器 flash 紧张时用 TUNNELEXIT_SKIP_GEOIP=1 全部跳过，或用 TUNNELEXIT_SKIP_GEOIP_V6=1 只留 IPv4 库
if ($roleConfig.IncludeGeoIP) {
    Copy-TunnelExitGeoIP -RepoRoot $Root -DestDir (Join-Path $DataRoot ($AssetDir -replace '/', '\'))
}

# 文本文件统一转 LF（无 BOM），防止 CRLF 破坏 shebang 与 UCI 配置
# 只处理包内文本资源，二进制与库文件不能走 ReadAllText/WriteAllText，会损坏内容
$textFiles = $assetFiles | ForEach-Object { Join-Path $DataRoot ($_ -replace '/', '\') }
foreach ($f in $textFiles) {
    $content = [System.IO.File]::ReadAllText($f)
    $content = $content -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($f, $content, [System.Text.UTF8Encoding]::new($false))
}

# LuCI 页面按前端资源压缩，与便携包保持一致的体积优化
Optimize-TextAsset (Join-Path $DataRoot ('www\luci-static\resources\view\' + $PackageName + '.js'))

Write-Host '[2/4] 生成 control 元数据 ...' -ForegroundColor Cyan

# 包元数据：Go 侧关闭 CGO 静态链接，因此不依赖 libc
# 描述的后续行按 opkg 约定缩进一个空格
$descriptionTail = ($roleConfig.DescriptionTail | ForEach-Object { ' ' + $_ }) -join "`n"
$control = @"
Package: $PackageName
Version: $Version
Architecture: $PackageArch
Maintainer: zgcwkj
Section: net
Priority: optional
Installed-Size: $([Math]::Ceiling((Get-Item (Join-Path $DataRoot ($BinaryRelPath -replace '/', '\'))).Length / 1KB))
Description: $($roleConfig.Description)
$descriptionTail
"@
[System.IO.File]::WriteAllText((Join-Path $ControlRoot 'control'), ($control.Trim() + "`n"), [System.Text.UTF8Encoding]::new($false))

# conffiles 声明用户可改的配置文件，opkg 升级时保留本地修改
# 客户端没有随包分发的配置（配置在首次运行时生成），因此不声明
if ($roleConfig.HasUciConfig) {
    [System.IO.File]::WriteAllText((Join-Path $ControlRoot 'conffiles'), ("/etc/config/$($roleConfig.InitScript)`n"), [System.Text.UTF8Encoding]::new($false))
}

# postinst 修正脚本权限、创建持久化数据目录并刷新 LuCI 菜单与权限缓存
# 两个角色都带 LuCI 入口，缓存刷新失败不影响安装
$postinst = @"
#!/bin/sh

chmod +x /$AssetDir/run.sh 2>/dev/null
chmod +x /etc/init.d/$($roleConfig.InitScript) 2>/dev/null
chmod +x /$BinaryRelPath 2>/dev/null

# 首次安装时创建持久化数据目录
mkdir -p /$DataDir 2>/dev/null

# 刷新 LuCI 菜单与权限缓存，失败不影响安装
[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd reload >/dev/null 2>&1
rm -f /tmp/luci-indexcache /tmp/luci-modulecache/* 2>/dev/null

exit 0
"@
[System.IO.File]::WriteAllText((Join-Path $ControlRoot 'postinst'), ($postinst.TrimStart() + "`n"), [System.Text.UTF8Encoding]::new($false))

# prerm 卸载前停掉服务
$prerm = @"
#!/bin/sh

[ -x /etc/init.d/$($roleConfig.InitScript) ] && /etc/init.d/$($roleConfig.InitScript) stop >/dev/null 2>&1
exit 0
"@
[System.IO.File]::WriteAllText((Join-Path $ControlRoot 'prerm'), ($prerm.TrimStart() + "`n"), [System.Text.UTF8Encoding]::new($false))

$controlFiles = @((Join-Path $ControlRoot 'control'), (Join-Path $ControlRoot 'postinst'), (Join-Path $ControlRoot 'prerm'))
if (Test-Path (Join-Path $ControlRoot 'conffiles')) {
    $controlFiles += (Join-Path $ControlRoot 'conffiles')
}
foreach ($f in $controlFiles) {
    $content = [System.IO.File]::ReadAllText($f) -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($f, $content, [System.Text.UTF8Encoding]::new($false))
}

Write-Host '[3/4] 生成 .ipk ...' -ForegroundColor Cyan

$controlTar = Join-Path $WorkDir 'control.tar.gz'
$dataTar = Join-Path $WorkDir 'data.tar.gz'
New-TarGz -SourceDir $ControlRoot -OutPath $controlTar -ExecSet @{ 'postinst' = $true; 'prerm' = $true }
$dataExecSet = @{
    $BinaryRelPath                       = $true
    ($AssetDir + '/run.sh')              = $true
    ('etc/init.d/' + $roleConfig.InitScript) = $true
}
New-TarGz -SourceDir $DataRoot -OutPath $dataTar -ExecSet $dataExecSet

# ar 成员头固定 60 字节：名称 16 + 时间 12 + 属主 6 + 属组 6 + 权限 8 + 长度 10 + 魔数 2
function Write-ArMember {
    param(
        [System.IO.BinaryWriter]$Writer,
        [string]$Name,
        [byte[]]$Data
    )
    # 尾部是 ar 要求的固定魔数 "` + 换行
    $header = ('{0,-16}{1,-12}{2,-6}{3,-6}{4,-8}{5,-10}' -f $Name, 0, 0, 0, 100644, $Data.Length) + '`' + "`n"
    $headerBytes = [System.Text.Encoding]::ASCII.GetBytes($header)
    if ($headerBytes.Length -ne 60) {
        throw "ar 成员头长度错误: $Name ($($headerBytes.Length))，应为 60"
    }
    $Writer.Write($headerBytes)
    $Writer.Write($Data)
    # ar 要求成员按偶数长度对齐，奇数长度补一个换行
    if (($Data.Length % 2) -ne 0) { $Writer.Write([byte]0x0A) }
}

$ipkName = Get-TunnelExitArtifactName -Role $Role -Version $Version -Flavor $PackageFlavor -Arch $Arch -Extension 'ipk'
$ipkPath = Join-Path $DistDir $ipkName

$ipkStream = [System.IO.File]::Open($ipkPath, [System.IO.FileMode]::Create)
$ipkWriter = [System.IO.BinaryWriter]::new($ipkStream, [System.Text.Encoding]::ASCII, $true)
try {
    $ipkWriter.Write([System.Text.Encoding]::ASCII.GetBytes("!<arch>`n"))
    Write-ArMember -Writer $ipkWriter -Name 'debian-binary' -Data ([System.Text.Encoding]::ASCII.GetBytes("2.0`n"))
    Write-ArMember -Writer $ipkWriter -Name 'control.tar.gz' -Data ([System.IO.File]::ReadAllBytes($controlTar))
    Write-ArMember -Writer $ipkWriter -Name 'data.tar.gz' -Data ([System.IO.File]::ReadAllBytes($dataTar))
}
finally {
    $ipkWriter.Dispose()
    $ipkStream.Dispose()
}

Write-Host '[4/4] 验证产物 ...' -ForegroundColor Cyan

$sizeMB = [Math]::Round((Get-Item $ipkPath).Length / 1MB, 2)
Write-Host "产物: $ipkPath ($sizeMB MB)" -ForegroundColor Green

if (-not $SkipVerify) {
    # Windows 没有 ar 命令，直接解析 ar 头部核对成员名与长度
    Write-Host "`n===== .ipk ar 成员 ====="
    $bytes = [System.IO.File]::ReadAllBytes($ipkPath)
    $magic = [System.Text.Encoding]::ASCII.GetString($bytes, 0, 8)
    if ($magic -ne "!<arch>`n") { throw "ipk 头部不正确: $magic" }
    $offset = 8
    while ($offset + 60 -le $bytes.Length) {
        $header = [System.Text.Encoding]::ASCII.GetString($bytes, $offset, 60)
        $memberName = $header.Substring(0, 16).Trim()
        $memberSize = [int]$header.Substring(48, 10).Trim()
        Write-Host ("  {0,-16} {1,10} 字节" -f $memberName, $memberSize)
        $offset += 60 + $memberSize
        if (($memberSize % 2) -ne 0) { $offset++ }
    }

    Write-Host "`n===== data.tar.gz 内容 ====="
    tar.exe -tzvf $dataTar
    Write-Host "`n===== control.tar.gz 内容 ====="
    tar.exe -tzvf $controlTar
}
