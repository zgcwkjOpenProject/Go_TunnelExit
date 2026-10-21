#Requires -Version 7.0
<#
.SYNOPSIS
构建 TunnelExit Android 客户端 APK

.DESCRIPTION
本工具在 Android 上只需要客户端：把 common/cmd/client 交叉编译成
android/arm64 可执行文件，重命名为 libtunnelexit-client.so 放入 jniLibs，
再由 Gradle 打包成 APK。命名成 lib*.so 是因为 Android 只允许从应用的
native 库目录（nativeLibraryDir）执行文件。

Android 不分服务端与客户端角色：只出客户端包，产物名以 tunnelexit-client- 开头，
可调参数也只有是否校验签名，没有 -Arch 与 -Role

由于 Android 的纯 Go 解析器无法读取系统的 DNS 配置，编译时必须开启
CGO 并使用 NDK 的 clang 作为 CC。

.EXAMPLE
.\platforms\build-android.ps1
.EXAMPLE
.\platforms\build-android.ps1 -SkipVerify
#>
[CmdletBinding()]
param(
    # 不做 zipalign/apksigner 校验，仅用于快速调试
    [switch]$SkipVerify
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# ---------- 可调参数：构建环境 ----------
# 默认值按本机环境填写，也支持用环境变量覆盖，便于 CI 复用
$AndroidSdk = if ($env:ANDROID_HOME) { $env:ANDROID_HOME }
elseif ($env:ANDROID_SDK_ROOT) { $env:ANDROID_SDK_ROOT }
else { 'D:\Application\Android\AndroidSDK' }
$JavaHome = if ($env:JAVA_HOME) { $env:JAVA_HOME } else { 'D:\Application\Java\jdk-21.0.2' }
$NdkVersion = '27.0.12077973'
$BuildToolsVersion = '36.0.0'
$ApiLevel = 30
$Abi = 'arm64-v8a'
$GoOS = 'android'
$GoArch = 'arm64'

# ---------- 可调参数：签名 ----------
# 发布签名凭据放在 shell 目录下的 signing.local.ps1 里（不进版本库），
# 缺失时退回输出未签名 APK，签名的本地构建不因凭据变动而失败
# 本机签名凭据与其模板，凭据文件由 .gitignore 忽略
$SigningFile = Join-Path $PSScriptRoot 'android\signing.local.ps1'
$SigningExampleFile = Join-Path $PSScriptRoot 'android\signing.local.example.ps1'
$KeystorePath = ''
$KeyAlias = ''
$StorePassword = ''
$KeyPassword = ''
if (Test-Path -LiteralPath $SigningFile) {
    . $SigningFile
}

# ---------- 路径 ----------
$PlatformRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = Split-Path -Parent $PlatformRoot
$Common = Join-Path $Root 'common'
$VersionFile = Join-Path $Common 'version'
$ShellDir = Join-Path $PlatformRoot 'android'
$BuildRoot = Join-Path $Root 'build'
$WorkDir = Join-Path $BuildRoot 'android'
$DistDir = Join-Path $Root 'dist'

. (Join-Path $PlatformRoot 'build-common.ps1')

$Version = Get-TunnelExitVersion -VersionFile $VersionFile
Write-Host "[version] 发布版本: $Version" -ForegroundColor Green

# Android 只有客户端，直接按客户端前缀拼出唯一产物名，全脚本只此一处
$ArtifactName = Get-TunnelExitArtifactName -Role 'client' -Version $Version -Flavor 'android' -Arch 'arm64' -Extension 'apk'

# 校验构建环境：SDK / NDK / JDK / build-tools 缺一不可，提前失败比中途报错更好定位
function Test-AndroidEnvironment {
    if (-not (Test-Path $AndroidSdk)) { throw "未找到 Android SDK: $AndroidSdk（可用 ANDROID_HOME 覆盖）" }
    if (-not (Test-Path $JavaHome)) { throw "未找到 JDK: $JavaHome（可用 JAVA_HOME 覆盖）" }

    $script:NdkDir = Join-Path $AndroidSdk "ndk\$NdkVersion"
    if (-not (Test-Path $NdkDir)) { throw "未找到 NDK ${NdkVersion}：$NdkDir" }

    $script:BuildToolsDir = Join-Path $AndroidSdk "build-tools\$BuildToolsVersion"
    if (-not (Test-Path $BuildToolsDir)) { throw "未找到 build-tools ${BuildToolsVersion}：$BuildToolsDir" }

    $clangName = "aarch64-linux-android$ApiLevel-clang.cmd"
    $script:ClangPath = Join-Path $NdkDir "toolchains\llvm\prebuilt\windows-x86_64\bin\$clangName"
    if (-not (Test-Path $ClangPath)) { throw "未找到 NDK clang：$ClangPath" }

    $script:JavaExe = Join-Path $JavaHome 'bin\java.exe'
    if (-not (Test-Path $JavaExe)) { throw "未找到 java：$JavaExe" }

    Write-Host "[env] SDK    = $AndroidSdk" -ForegroundColor DarkGray
    Write-Host "[env] NDK    = $NdkDir" -ForegroundColor DarkGray
    Write-Host "[env] JDK    = $JavaHome" -ForegroundColor DarkGray
    Write-Host "[env] clang  = $ClangPath" -ForegroundColor DarkGray
}

# 把 Android 外壳复制到 build/android，并写入 local.properties / 客户端二进制
function New-AndroidStage {
    if (-not (Test-Path (Join-Path $Common 'go.mod'))) { throw '缺少 common/go.mod' }
    if (-not (Test-Path $ShellDir)) { throw "缺少 Android 外壳目录: $ShellDir" }

    Remove-TunnelExitStage -Path $WorkDir -BuildRoot $BuildRoot
    New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null
    # 整个外壳复制过去，wrapper、图标、Java 源码都在其中
    Copy-Item (Join-Path $ShellDir '*') $WorkDir -Recurse -Force
    # .gitignore 是隐藏文件，通配符匹配不到，单独处理
    $gitignore = Join-Path $ShellDir '.gitignore'
    if (Test-Path $gitignore) { Copy-Item $gitignore $WorkDir -Force }

    # Gradle 依赖固定的 SDK 位置，写死路径避免它对环境变量产生歧义
    $localProps = "sdk.dir=$($AndroidSdk -replace '\\', '\\')`n"
    [System.IO.File]::WriteAllText((Join-Path $WorkDir 'local.properties'), $localProps, [System.Text.UTF8Encoding]::new($false))
    Write-Host "[stage] Gradle 工程已就绪: $WorkDir" -ForegroundColor DarkGray
}

# 编译客户端二进制到 jniLibs，文件名必须是 lib*.so 才能被 Android 允许执行
function Build-AndroidBinary {
    $jniDir = Join-Path $WorkDir "app\src\main\jniLibs\$Abi"
    Invoke-TunnelExitBuild -SourceDir $Common -OutPath (Join-Path $jniDir 'libtunnelexit-client.so') `
        -Package './cmd/client' -GOOS $GoOS -GOARCH $GoArch -GOARM '' `
        -Version $Version -Platform 'android_arm64' `
        -EnableCGO -CCompiler $ClangPath
}

# 调用 Gradle 打包 release APK
function Invoke-GradleBuild {
    $gradlew = Join-Path $WorkDir 'gradlew.bat'
    if (-not (Test-Path $gradlew)) { throw "缺少 Gradle wrapper: $gradlew" }
    $env:JAVA_HOME = $JavaHome
    $env:ANDROID_HOME = $AndroidSdk
    $env:ANDROID_SDK_ROOT = $AndroidSdk
    Write-Host '[gradle] 开始组装 Release APK…' -ForegroundColor Cyan
    & $gradlew -p $WorkDir ':app:assembleRelease' --no-daemon "--console=plain" "-PappVersion=$Version"
    if ($LASTEXITCODE -ne 0) { throw "Gradle 构建失败，退出码 $LASTEXITCODE" }
}

# 对齐并签名，未配置 keystore 时直接输出 Gradle 产出的未签名 APK
function Optimize-AndroidApk {
    $unsigned = Join-Path $WorkDir 'app\build\outputs\apk\release'
    $apk = Get-ChildItem -Path $unsigned -Filter '*.apk' -File -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $apk) { throw "Gradle 未产出 APK: $unsigned" }

    $outApk = Join-Path $DistDir $ArtifactName
    New-Item -ItemType Directory -Path $DistDir -Force | Out-Null

    if ($KeystorePath -eq '' -or -not (Test-Path -LiteralPath $KeystorePath)) {
        if ($KeystorePath -eq '') {
            Write-Host "[warn] 未配置发布签名凭据，可复制 $SigningExampleFile 为 signing.local.ps1 填写，本次直接输出未签名 APK" -ForegroundColor Yellow
        }
        else {
            Write-Host "[warn] 未找到签名证书 $KeystorePath，直接输出未签名 APK" -ForegroundColor Yellow
        }
        Copy-Item $apk.FullName $outApk -Force
        return
    }

    $zipalign = Join-Path $BuildToolsDir 'zipalign.exe'
    $apksigner = Join-Path $BuildToolsDir 'apksigner.bat'
    $aligned = Join-Path $WorkDir 'app-aligned.apk'

    Write-Host '[apk] zipalign 对齐…' -ForegroundColor Cyan
    & $zipalign -f -p 4 $apk.FullName $aligned
    if ($LASTEXITCODE -ne 0) { throw "zipalign 失败，退出码 $LASTEXITCODE" }

    Write-Host '[apk] apksigner 签名…' -ForegroundColor Cyan
    & $apksigner sign --v4-signing-enabled false `
        --ks $KeystorePath --ks-key-alias $KeyAlias `
        --ks-pass "pass:$StorePassword" --key-pass "pass:$KeyPassword" `
        --out $outApk $aligned
    if ($LASTEXITCODE -ne 0) { throw "apksigner 签名失败，退出码 $LASTEXITCODE" }

    if (-not $SkipVerify) {
        Write-Host '[apk] 校验签名…' -ForegroundColor Cyan
        & $apksigner verify --verbose --print-certs $outApk
        if ($LASTEXITCODE -ne 0) { throw "apksigner 校验失败，退出码 $LASTEXITCODE" }
    }
    Remove-Item $aligned -Force -ErrorAction SilentlyContinue
}

# 输出产物信息
function Write-AndroidSummary {
    $outApk = Join-Path $DistDir $ArtifactName
    if (-not (Test-Path $outApk)) { throw "未找到产物: $outApk" }
    $size = [Math]::Round((Get-Item $outApk).Length / 1MB, 2)
    Write-Host ''
    Write-Host "[done] Android APK 构建完成: $(Split-Path -Leaf $outApk) ($size MB)" -ForegroundColor Green
    Write-Host "       输出目录: $DistDir" -ForegroundColor Green
}

Test-AndroidEnvironment
New-AndroidStage
Build-AndroidBinary
Invoke-GradleBuild
Optimize-AndroidApk
Write-AndroidSummary
