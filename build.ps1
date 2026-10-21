#Requires -Version 7.0
<#
.SYNOPSIS
统一构建 TunnelExit 的全部发布产物

.DESCRIPTION
执行时会交互选择要运行的平台构建脚本，也可以选择全部

.EXAMPLE
.\build.ps1
#>
[CmdletBinding()]
param(
    [string]$Only
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$Platforms = Join-Path $Root 'platforms'
$PowerShell = (Get-Command pwsh -ErrorAction Stop).Source
$VersionFile = Join-Path $Root 'common\version'

# 读取所有发布构建共用的唯一版本号
if (-not (Test-Path -LiteralPath $VersionFile -PathType Leaf)) {
    throw '无法确定版本号：缺少 common/version'
}
$ReleaseVersion = (Get-Content -LiteralPath $VersionFile -Raw).Trim()
if ([string]::IsNullOrWhiteSpace($ReleaseVersion)) {
    throw '无法确定版本号：common/version 为空'
}
Write-Host "[version] 发布版本: $ReleaseVersion" -ForegroundColor Green

# 动态发现平台构建脚本，避免新增平台时还要修改统一入口
$discoveredScripts = @(Get-ChildItem -LiteralPath $Platforms -Filter 'build-*.ps1' -File |
        Where-Object { $_.Name -ne 'build-common.ps1' } |
        Sort-Object Name)
if ($discoveredScripts.Count -eq 0) {
    throw 'platforms/ 下未找到可执行的 build-*.ps1 脚本'
}

# 支持 -Only 直接指定平台，便于脚本化调用
if (-not [string]::IsNullOrWhiteSpace($Only)) {
    $wanted = $Only -split '[,\s]+' | Where-Object { $_ -ne '' }
    $buildScripts = @()
    foreach ($item in $wanted) {
        $matched = $discoveredScripts | Where-Object { $_.Name -like "*$item*" }
        if (-not $matched) { throw "未找到匹配的平台构建脚本：$item" }
        $buildScripts += $matched
    }
}
else {
    Write-Host "`n可用的平台构建脚本：" -ForegroundColor Cyan
    Write-Host '  0. 全部平台'
    for ($index = 0; $index -lt $discoveredScripts.Count; $index++) {
        Write-Host ("  {0}. {1}" -f ($index + 1), $discoveredScripts[$index].Name)
    }
    $selection = Read-Host '请输入编号（可用逗号或空格分隔，默认 0）'
    if ([string]::IsNullOrWhiteSpace($selection) -or $selection.Trim() -eq '0') {
        $buildScripts = @($discoveredScripts)
    }
    else {
        $selectedIndexes = [System.Collections.Generic.HashSet[int]]::new()
        $selectionItems = $selection -split '[,\s]+' | Where-Object { $_ -ne '' }
        foreach ($item in $selectionItems) {
            $number = 0
            if (-not [int]::TryParse($item, [ref]$number) -or $number -lt 1 -or $number -gt $discoveredScripts.Count) {
                throw "无效的平台编号：$item，请输入 0 或 1-$($discoveredScripts.Count)"
            }
            [void]$selectedIndexes.Add($number - 1)
        }
        $buildScripts = @(
            for ($index = 0; $index -lt $discoveredScripts.Count; $index++) {
                if ($selectedIndexes.Contains($index)) { $discoveredScripts[$index] }
            }
        )
    }
}

foreach ($script in $buildScripts) {
    $name = Join-Path 'platforms' $script.Name
    $scriptPath = $script.FullName
    if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
        throw "未找到构建脚本: $scriptPath"
    }
    Write-Host "`n===== 开始 $name =====" -ForegroundColor Cyan
    & $PowerShell -NoProfile -ExecutionPolicy Bypass -File $scriptPath
    if ($LASTEXITCODE -ne 0) {
        throw "$name 构建失败，退出码 $LASTEXITCODE"
    }
}

Write-Host "`n选定平台构建完成，产物位于 dist/" -ForegroundColor Green
