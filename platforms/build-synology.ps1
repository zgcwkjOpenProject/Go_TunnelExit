#Requires -Version 7.0
<#
.SYNOPSIS
    在 Windows 上打包 TunnelExit 的群晖（Synology DSM 7）套件 .spk

.DESCRIPTION
    服务端套件编译 common/ 生成 tunnelexit-server，客户端套件生成 tunnelexit-client，
    再把 platforms/synology/ 下对应角色的套件元数据、生命周期脚本、DSM 配置和编译
    产物组装到 build/synology/ 暂存目录，用 .NET System.Formats.Tar 生成 tar 并显式
    写入 Unix 权限位（脚本与二进制可执行，普通文件默认权限），文本文件统一转成 LF
    行尾，解决 Windows 打包的常见坑。产物按角色输出到 dist/：tunnelexit-* 是服务端套件，
    tunnelexit-client-* 是客户端套件

    一个套件只装一个角色：NAS 作为隧道入口时装服务端套件，需要让 NAS 借用别的机器
    的网络出口时装客户端套件，机器缺哪个角色就取同版本的另一个包

.PARAMETER Arch
    目标架构，默认生成全部架构

.PARAMETER Role
    套件角色，默认生成服务端与客户端两个套件

.PARAMETER SkipVerify
    跳过产物内容打印

    版本号从 common/version 读取，无需也无法通过参数覆盖

.EXAMPLE
    .\platforms\build-synology.ps1
    .\platforms\build-synology.ps1 -Arch x86_64
    .\platforms\build-synology.ps1 -Role client
#>
[CmdletBinding()]
param(
    [ValidateSet('all', 'x86_64', 'armv8')]
    [string]$Arch = 'all',
    [ValidateSet('all', 'server', 'client')]
    [string]$Role = 'all',
    [switch]$SkipVerify
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# 构建配置
$GoOS = 'linux'
$PackageFlavor = 'synology'
$PackageIconNames = @('PACKAGE_ICON.PNG', 'PACKAGE_ICON_256.PNG')

# 仓库与构建路径
$PlatformRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = Split-Path -Parent $PlatformRoot
$SynoDir = Join-Path $PlatformRoot 'synology'
$Common = Join-Path $Root 'common'
$DistDir = Join-Path $Root 'dist'
$BuildRoot = Join-Path $Root 'build'
$VersionFile = Join-Path $Common 'version'
$BuildCommonScript = Join-Path $PlatformRoot 'build-common.ps1'
$GoModuleFile = Join-Path $Common 'go.mod'
# 图标与 README 两个角色共用，各自的 .spk 里都要装一份
$PackageIconSource = Join-Path $SynoDir $PackageIconNames[0]
$PackageIcon256Source = Join-Path $SynoDir $PackageIconNames[1]

# 架构与角色都取默认值时逐个组合，其余调用复用同一套打包流程
if ($Arch -eq 'all' -or $Role -eq 'all') {
    $archTargets = if ($Arch -eq 'all') { @('x86_64', 'armv8') } else { @($Arch) }
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
    x86_64 = @{ PackageArch = 'x86_64'; GoArch = 'amd64' }
    armv8  = @{ PackageArch = 'armv8'; GoArch = 'arm64' }
}[$Arch]
$PackageArch = $archConfig.PackageArch
$GoArch = $archConfig.GoArch

# 角色差异：源码目录、Go 入口、二进制名、套件内配置、归属地库与 DSM 桌面入口
$roleConfig = @{
    server = @{
        GoCmd        = './cmd/server'
        Binary       = 'tunnelexit-server'
        ConfigFile   = 'server.json'
        SourceRoot   = Join-Path $SynoDir 'server'
        IncludeGeoIP = $true
        IncludeUI    = $false
    }
    client = @{
        GoCmd        = './cmd/client'
        Binary       = 'tunnelexit-client'
        ConfigFile   = 'client.json'
        SourceRoot   = Join-Path $SynoDir 'client'
        IncludeGeoIP = $false
        IncludeUI    = $true
    }
}[$Role]
# 两个角色各用独立暂存目录，避免相互覆盖
$WorkDir = Join-Path $BuildRoot ('synology/' + $Role)
$Payload = Join-Path $WorkDir 'payload'
$SpkRoot = Join-Path $WorkDir 'spkroot'
$PkgTgz = Join-Path $SpkRoot 'package.tgz'
$InfoTemplate = Join-Path $roleConfig.SourceRoot 'INFO'
$PackageSource = Join-Path $roleConfig.SourceRoot 'package'
$ScriptsSource = Join-Path $roleConfig.SourceRoot 'scripts'
$ConfSource = Join-Path $roleConfig.SourceRoot 'conf'
$WizardSource = Join-Path $roleConfig.SourceRoot 'WIZARD_UIFILES'

# 前置检查
try { Add-Type -AssemblyName System.Formats.Tar -ErrorAction Stop }
catch { throw '需要 PowerShell 7+（依赖 System.Formats.Tar）。请用 pwsh 运行本脚本。' }

if (-not (Test-Path $GoModuleFile)) {
    throw '缺少 common/go.mod'
}
foreach ($p in @($PackageSource, $ScriptsSource, $ConfSource, $WizardSource, $PackageIconSource, $PackageIcon256Source)) {
    if (-not (Test-Path $p)) { throw "缺少必需文件或目录: $p" }
}

. $BuildCommonScript

$Version = Get-TunnelExitVersion -VersionFile $VersionFile

Remove-TunnelExitStage -Path $WorkDir -BuildRoot $BuildRoot
New-Item -ItemType Directory -Path $DistDir -Force | Out-Null
New-Item -ItemType Directory -Path $Payload | Out-Null
New-Item -ItemType Directory -Path $SpkRoot | Out-Null

Write-Host '[1/4] 编译二进制并组装 payload ...' -ForegroundColor Cyan

New-Item -ItemType Directory -Path (Join-Path $Payload 'app') | Out-Null
Invoke-TunnelExitBuild -SourceDir $Common -OutPath (Join-Path $Payload ('app\' + $roleConfig.Binary)) `
    -Package $roleConfig.GoCmd -GOOS $GoOS -GOARCH $GoArch `
    -Version $Version -Platform ("synology_{0}" -f $PackageArch)

# 群晖专用文件
Copy-Item (Join-Path $PackageSource 'app/start.sh') (Join-Path $Payload 'app\start.sh')
# 客户端才有 DSM 桌面图标与 Webman 反代入口，两者必须同属 package.tgz 的根目录
if ($roleConfig.IncludeUI) {
    Copy-Item -Recurse (Join-Path $PackageSource 'ui') (Join-Path $Payload 'ui')
    Copy-Item -Recurse (Join-Path $PackageSource 'etc') (Join-Path $Payload 'etc')

    # 桌面入口展示的版本号跟随 common/version，避免模板漏同步
    $uiConfigPath = Join-Path $Payload 'ui\config'
    $uiConfig = Get-Content -Raw $uiConfigPath | ConvertFrom-Json
    $uiConfig.'Main.js'.'SYNO.SDS.TunnelExit.Instance'.version = $Version
    $uiConfig | ConvertTo-Json -Depth 10 | Set-Content -Path $uiConfigPath -Encoding utf8NoBOM
}

# 运行参数随套件一起安装，位置与 start.sh 的搬运逻辑一致
$configPath = Join-Path $Payload ('app\' + $roleConfig.ConfigFile)
if ($Role -eq 'server') {
    Write-TunnelExitServerConfig $configPath
}
else {
    Write-TunnelExitClientConfig $configPath
}

# 归属地展示只有服务端用得到，库文件只随服务端套件分发
# start.sh 的 TUNNELEXIT_APP_DIR 正指向二进制所在目录
if ($roleConfig.IncludeGeoIP) {
    Copy-TunnelExitGeoIP -RepoRoot $Root -DestDir (Join-Path $Payload 'app')
}

# spk 根目录文件
Copy-Item -Recurse $ScriptsSource (Join-Path $SpkRoot 'scripts')
Copy-Item -Recurse $ConfSource (Join-Path $SpkRoot 'conf')
Copy-Item -Recurse $WizardSource (Join-Path $SpkRoot 'WIZARD_UIFILES')
Copy-Item $PackageIconSource (Join-Path $SpkRoot $PackageIconNames[0])
Copy-Item $PackageIcon256Source (Join-Path $SpkRoot $PackageIconNames[1])

# INFO：注入版本和架构
$info = Get-Content -Raw $InfoTemplate
# (?m) 让 ^ 锚定每行行首：version=/arch= 都不在文件首行，默认单行模式会静默漏匹配
$info = $info -replace '(?m)^version="[^"]*"', ('version="' + $Version + '"')
$info = $info -replace '(?m)^arch="[^"]*"', ('arch="' + $PackageArch + '"')
[System.IO.File]::WriteAllText((Join-Path $SpkRoot 'INFO'), $info, [System.Text.UTF8Encoding]::new($false))

# 文本文件统一转 LF（无 BOM），防止 CRLF 破坏 shebang 和 JSON
$textFiles = [System.Collections.Generic.List[string]]::new()
Get-ChildItem (Join-Path $SpkRoot 'scripts') -File | ForEach-Object { $textFiles.Add($_.FullName) }
Get-ChildItem (Join-Path $SpkRoot 'conf') -Recurse -File | ForEach-Object { $textFiles.Add($_.FullName) }
Get-ChildItem (Join-Path $SpkRoot 'WIZARD_UIFILES') -File | ForEach-Object { $textFiles.Add($_.FullName) }
$textFiles.Add((Join-Path $SpkRoot 'INFO'))
$textFiles.Add((Join-Path $Payload 'app\start.sh'))
$textFiles.Add($configPath)
if ($roleConfig.IncludeUI) {
    $textFiles.Add((Join-Path $Payload 'ui\config'))
    $textFiles.Add((Join-Path $Payload 'etc\www\tunnelexit-client.nginx.conf'))
}

foreach ($f in $textFiles) {
    $content = [System.IO.File]::ReadAllText($f)
    $content = $content -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($f, $content, [System.Text.UTF8Encoding]::new($false))
}

# 桌面入口的 JS 只由 DSM 加载，同样按普通文本资源压缩
if ($roleConfig.IncludeUI) {
    Optimize-TextAssets (Join-Path $Payload 'ui')
}

Write-Host '[2/4] 生成 package.tgz ...' -ForegroundColor Cyan

# 二进制与启动脚本需要可执行权限，其余文件按普通文件权限写入
$execSet = @{ 'app/start.sh' = $true; ('app/' + $roleConfig.Binary) = $true }
New-TarGz -SourceDir $Payload -OutPath $PkgTgz -ExecSet $execSet

Write-Host '[3/4] 生成 .spk ...' -ForegroundColor Cyan

$spkName = Get-TunnelExitArtifactName -Role $Role -Version $Version -Flavor $PackageFlavor -Arch $PackageArch -Extension 'spk'
$spkPath = Join-Path $DistDir $spkName

$spkFs = [System.IO.File]::Open($spkPath, [System.IO.FileMode]::Create)
$spkWriter = [System.Formats.Tar.TarWriter]::new($spkFs, $false)
try {
    Add-TarFile -Writer $spkWriter -Source (Join-Path $SpkRoot 'INFO') -Name 'INFO'
    Add-TarFile -Writer $spkWriter -Source (Join-Path $SpkRoot 'PACKAGE_ICON.PNG') -Name 'PACKAGE_ICON.PNG'
    Add-TarFile -Writer $spkWriter -Source (Join-Path $SpkRoot 'PACKAGE_ICON_256.PNG') -Name 'PACKAGE_ICON_256.PNG'
    Add-TarFile -Writer $spkWriter -Source $PkgTgz -Name 'package.tgz'

    Add-TarDirectory -Writer $spkWriter -Name 'scripts'
    Get-ChildItem (Join-Path $SpkRoot 'scripts') -File | ForEach-Object {
        Add-TarFile -Writer $spkWriter -Source $_.FullName -Name "scripts/$($_.Name)" -ModeOctal '755'
    }

    Add-TarDirectory -Writer $spkWriter -Name 'conf'
    Add-TarDirectory -Writer $spkWriter -Name 'conf/systemd'
    Get-ChildItem (Join-Path $SpkRoot 'conf') -Recurse -File | ForEach-Object {
        $rel = $_.FullName.Substring((Join-Path $SpkRoot 'conf').Length + 1) -replace '\\', '/'
        Add-TarFile -Writer $spkWriter -Source $_.FullName -Name "conf/$rel"
    }

    Add-TarDirectory -Writer $spkWriter -Name 'WIZARD_UIFILES'
    Get-ChildItem (Join-Path $SpkRoot 'WIZARD_UIFILES') -File | Sort-Object Name | ForEach-Object {
        Add-TarFile -Writer $spkWriter -Source $_.FullName -Name "WIZARD_UIFILES/$($_.Name)"
    }
}
finally {
    $spkWriter.Dispose()
}

Write-Host '[4/4] 验证产物 ...' -ForegroundColor Cyan

$sizeMB = [Math]::Round((Get-Item $spkPath).Length / 1MB, 2)
Write-Host "产物: $spkPath ($sizeMB MB)" -ForegroundColor Green

if (-not $SkipVerify) {
    Write-Host "`n===== .spk 内容 ====="
    tar.exe -tvf $spkPath

    Write-Host "`n===== package.tgz 前 15 条 ====="
    tar.exe -xf $spkPath -C $WorkDir 'package.tgz'
    tar.exe -tzvf (Join-Path $WorkDir 'package.tgz') | Select-Object -First 15
}
