#Requires -Version 7.0
<#
.SYNOPSIS
    在 Windows 上打包 TunnelExit 的飞牛（fnOS）套件 .fpk

.DESCRIPTION
    服务端套件编译 common/ 生成 tunnelexit-server，客户端套件生成 tunnelexit-client，
    再分别与 platforms/fnos/ 下对应角色的套件元数据、生命周期脚本、向导组装到
    build/fnos/ 暂存目录，还原 fnpack 期望的源码布局后调用 fnpack.exe build，
    产物输出到 dist/：tunnelexit-* 是服务端套件，tunnelexit-client-* 是客户端套件

    一个套件只装一个角色：NAS 作为隧道入口时装服务端套件，需要让 NAS 借用别的机器
    的网络出口时装客户端套件，机器缺哪个角色就取同版本的另一个包

.PARAMETER Arch
    目标架构，默认生成全部架构

.PARAMETER Role
    套件角色，默认生成服务端与客户端两个套件

.EXAMPLE
    .\platforms\build-fnos.ps1
    .\platforms\build-fnos.ps1 -Arch arm64
    .\platforms\build-fnos.ps1 -Role client
#>
[CmdletBinding()]
param(
    [ValidateSet('all', 'x86', 'arm64')]
    [string]$Arch = 'all',
    [ValidateSet('all', 'server', 'client')]
    [string]$Role = 'all'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# 构建配置
$FnpackDefaultVersion = '1.2.3'
$FnpackDocumentationUrl = 'https://developer.fnnas.com/docs/cli/fnpack/'
$FnpackDownloadUrl = 'https://static2.fnnas.com/fnpack/fnpack-{0}-windows-amd64'
$FnpackMinimumSize = 1MB
$GoOS = 'linux'

# 仓库与构建路径
$PlatformRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = Split-Path -Parent $PlatformRoot
$FnosDir = Join-Path $PlatformRoot 'fnos'
$Common = Join-Path $Root 'common'
$BuildRoot = Join-Path $Root 'build'
$DistDir = Join-Path $Root 'dist'
$Fnpack = Join-Path $Root 'fnpack.exe'
$VersionFile = Join-Path $Common 'version'
$BuildCommonScript = Join-Path $PlatformRoot 'build-common.ps1'
$GoModuleFile = Join-Path $Common 'go.mod'
# config/ 与图标两个角色共用，各自套件里都要装一份
$SharedConfigSource = Join-Path $FnosDir 'config'
$PackageIcons = @('ICON.PNG', 'ICON_256.PNG')
if (-not (Test-Path -LiteralPath $VersionFile -PathType Leaf)) { throw '无法确定版本号：缺少 common/version' }
$PackageVersion = (Get-Content -LiteralPath $VersionFile -Raw).Trim()
if ([string]::IsNullOrWhiteSpace($PackageVersion)) { throw '无法确定版本号：common/version 为空' }

# 架构与角色都取默认值时逐个组合，其余调用复用同一套打包流程
if ($Arch -eq 'all' -or $Role -eq 'all') {
    $archTargets = if ($Arch -eq 'all') { @('x86', 'arm64') } else { @($Arch) }
    $roleTargets = if ($Role -eq 'all') { @('server', 'client') } else { @($Role) }
    foreach ($targetArch in $archTargets) {
        foreach ($targetRole in $roleTargets) {
            & $PSCommandPath -Arch $targetArch -Role $targetRole
            if ($LASTEXITCODE -ne 0) { throw "$targetArch $targetRole 打包失败，退出码 $LASTEXITCODE" }
        }
    }
    exit 0
}

$archConfig = @{
    x86   = @{ Platform = 'x86'; GoArch = 'amd64'; OutputArch = 'x86'; PackageArch = 'x86_64' }
    arm64 = @{ Platform = 'arm'; GoArch = 'arm64'; OutputArch = 'arm64'; PackageArch = 'arm64' }
}[$Arch]
$PackagePlatform = $archConfig.Platform
$GoArch = $archConfig.GoArch
$OutputArch = $archConfig.OutputArch
# PackageArch 仅用于产物文件名，避免把 fnOS 的 platform 字段误解为 32 位架构
$PackageArch = $archConfig.PackageArch

# 角色差异：Go 入口、二进制名、套件内配置文件、源码目录、是否附带归属地库与桌面图标
$roleConfig = @{
    server = @{
        GoCmd        = './cmd/server'
        Binary       = 'tunnelexit-server'
        ConfigFile   = 'server.json'
        SourceDir    = Join-Path $FnosDir 'server'
        IncludeGeoIP = $true
        IncludeUI    = $true
    }
    client = @{
        GoCmd        = './cmd/client'
        Binary       = 'tunnelexit-client'
        ConfigFile   = 'client.json'
        SourceDir    = Join-Path $FnosDir 'client'
        IncludeGeoIP = $false
        IncludeUI    = $true
    }
}[$Role]
$RoleSourceDir = $roleConfig.SourceDir
$ManifestSource = Join-Path $RoleSourceDir 'manifest'
# 两个角色各用独立暂存目录，避免相互覆盖
$Stage = Join-Path $BuildRoot ('fnos/' + $Role)

# 确保用于飞牛打包的 fnpack 可执行文件可用
function Get-FnpackBinary {
    param([string]$Path)
    if (Test-Path $Path) { return }

    Write-Host '[fnpack] 未找到 fnpack.exe，开始自动下载 ...' -ForegroundColor Yellow

    $version = $FnpackDefaultVersion
    try {
        $docs = (Invoke-WebRequest -Uri $FnpackDocumentationUrl -TimeoutSec 20).Content
        $m = [regex]::Match($docs, 'fnpack-(\d+\.\d+\.\d+)-windows-amd64')
        if ($m.Success) { $version = $m.Groups[1].Value }
    }
    catch {
        Write-Warning "获取 fnpack 最新版本失败，使用固定版本 $version"
    }

    $url = $FnpackDownloadUrl -f $version
    Write-Host "[fnpack] 下载: $url"
    try {
        Invoke-WebRequest -Uri $url -OutFile $Path -TimeoutSec 300
    }
    catch {
        if (Test-Path $Path) { [System.IO.File]::Delete($Path) }
        throw "fnpack.exe 下载失败: $($_.Exception.Message)"
    }

    $size = (Get-Item $Path).Length
    if ($size -lt $FnpackMinimumSize) {
        [System.IO.File]::Delete($Path)
        throw "fnpack.exe 下载异常（仅 $size 字节），已删除，请手动下载官方版本后重试"
    }
    $probe = (& $Path --help 2>&1 | Out-String)
    if ($probe -notmatch 'Command-line utility') {
        [System.IO.File]::Delete($Path)
        throw '下载的 fnpack.exe 无法运行，已删除，请手动下载官方版本后重试'
    }
    Write-Host "[fnpack] 下载完成: $Path ($([Math]::Round($size / 1MB, 2)) MB)" -ForegroundColor Green
}

foreach ($p in @($FnosDir, $RoleSourceDir, $SharedConfigSource, $Common)) {
    if (-not (Test-Path $p)) { throw "缺少: $p" }
}
if (-not (Test-Path $GoModuleFile)) {
    throw '缺少 common/go.mod'
}
Get-FnpackBinary $Fnpack

# 公共函数：Go 交叉编译、tar.gz 打包
. $BuildCommonScript

Remove-TunnelExitStage -Path $Stage -BuildRoot $BuildRoot
New-Item -ItemType Directory -Path $DistDir -Force | Out-Null
$appStage = Join-Path $Stage 'app'
New-Item -ItemType Directory -Path $appStage | Out-Null

Write-Host "[1/4] 交叉编译 $($roleConfig.Binary) ..." -ForegroundColor Cyan
Invoke-TunnelExitBuild -SourceDir $Common -OutPath (Join-Path $appStage $roleConfig.Binary) `
    -Package $roleConfig.GoCmd -GOOS $GoOS -GOARCH $GoArch `
    -Version $PackageVersion -Platform ("fnos_{0}" -f $OutputArch)

Write-Host '[2/4] 组装飞牛源码 ...' -ForegroundColor Cyan

# 飞牛专用文件（还原 fnpack 期望的根目录布局）
Copy-Item -Recurse (Join-Path $RoleSourceDir 'cmd') (Join-Path $Stage 'cmd')
Copy-Item -Recurse $SharedConfigSource (Join-Path $Stage 'config')
Copy-Item -Recurse (Join-Path $RoleSourceDir 'wizard') (Join-Path $Stage 'wizard')
Copy-Item $ManifestSource (Join-Path $Stage 'manifest')
# 两个角色都有桌面图标入口，ui/ 里的图标尺寸与 ui/config 中引用的占位一致
# fnpack 要求桌面入口放在 app/ui 下，放在套件根目录会让打包报错
if ($roleConfig.IncludeUI) {
    Copy-Item -Recurse (Join-Path $RoleSourceDir 'ui') (Join-Path $appStage 'ui')
}
foreach ($icon in $PackageIcons) {
    Copy-Item (Join-Path $FnosDir $icon) (Join-Path $Stage $icon)
}

# manifest 的版本与平台字段必须与实际产物一致
$manifestPath = Join-Path $Stage 'manifest'
$manifest = [System.IO.File]::ReadAllText($manifestPath)
$manifest = $manifest -replace '(?m)^version=.*$', ('version=' + $PackageVersion)
$manifest = $manifest -replace '(?m)^platform=.*$', ('platform=' + $PackagePlatform)
[System.IO.File]::WriteAllText($manifestPath, $manifest, [System.Text.UTF8Encoding]::new($false))

# 运行参数落在套件目录内，随套件一起被平台管理
$configPath = Join-Path $appStage $roleConfig.ConfigFile
if ($Role -eq 'server') {
    Write-TunnelExitServerConfig $configPath
}
else {
    Write-TunnelExitClientConfig $configPath
}

# 归属地展示只有服务端用得到，库文件只随服务端套件分发
# 服务端 cmd/main 用 TUNNELEXIT_GEOIP_FILE 与 TUNNELEXIT_GEOIP_FILE_V6 指向本目录
if ($roleConfig.IncludeGeoIP) {
    Copy-TunnelExitGeoIP -RepoRoot $Root -DestDir $appStage
}

# 文本文件统一转 LF（无 BOM），防止 CRLF 破坏 shebang 和 JSON
# ui/ 下只有 config 是文本，图标是二进制，绝不能走 ReadAllText/WriteAllText，会损坏内容
$textFiles = [System.Collections.Generic.List[string]]::new()
Get-ChildItem (Join-Path $Stage 'cmd') -File | ForEach-Object { $textFiles.Add($_.FullName) }
Get-ChildItem (Join-Path $Stage 'config') -File | ForEach-Object { $textFiles.Add($_.FullName) }
Get-ChildItem (Join-Path $Stage 'wizard') -File | ForEach-Object { $textFiles.Add($_.FullName) }
if ($roleConfig.IncludeUI) {
    $textFiles.Add((Join-Path $appStage 'ui\config'))
}
$textFiles.Add((Join-Path $Stage 'manifest'))
$textFiles.Add($configPath)

foreach ($f in $textFiles) {
    $content = [System.IO.File]::ReadAllText($f)
    $content = $content -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($f, $content, [System.Text.UTF8Encoding]::new($false))
}

Write-Host '[3/4] 调用 fnpack 打包 ...' -ForegroundColor Cyan
Push-Location $Stage
try {
    & $Fnpack build -d .
    if ($LASTEXITCODE -ne 0) {
        throw "fnpack 构建失败，退出码 $LASTEXITCODE"
    }
}
finally {
    Pop-Location
}

$fpk = Get-ChildItem -LiteralPath $Stage -Filter '*.fpk' | Select-Object -First 1
if (-not $fpk) { throw '未找到 fnpack 输出的 .fpk 文件' }

$outName = Get-TunnelExitArtifactName -Role $Role -Version $PackageVersion -Flavor 'fnos' -Arch $PackageArch -Extension 'fpk'
$outPath = Join-Path $DistDir $outName
Copy-Item -LiteralPath $fpk.FullName $outPath -Force

Write-Host "[4/4] 完成: $outPath ($([Math]::Round((Get-Item $outPath).Length / 1MB, 2)) MB)" -ForegroundColor Green
