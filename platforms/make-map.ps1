#Requires -Version 7.0
<#
.SYNOPSIS
    生成服务端展示页内嵌的世界底图与落点坐标

.DESCRIPTION
    从 Natural Earth 110m 国家轮廓（公有领域数据）换算成一份 SVG 路径，并把各国标签坐标与
    中国省级坐标一并投影成页面坐标，输出 common/webui/assets/worldmap.js。
    底图随二进制内嵌，页面运行时不联网也不引用任何外部资源。
    投影采用墨卡托，纵向裁剪掉南极，横向覆盖全部经度。
    仓库提交的是生成结果，构建脚本不调用本脚本；调整投影参数后重新执行即可。

.EXAMPLE
    ./platforms/make-map.ps1
    使用脚本内置的默认参数重新生成底图
#>
[CmdletBinding()]
param(
    [string]$SourceUrl = 'https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_admin_0_countries.geojson',
    [double]$Width = 1000,
    [double]$TopLatitude = 84,
    [double]$BottomLatitude = -58,
    [int]$Precision = 1
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$PlatformRoot = $PSScriptRoot
$RepoRoot = Split-Path -Parent $PlatformRoot
$ScratchDir = Join-Path $RepoRoot 'build/map-scratch'
$SourceFile = Join-Path $ScratchDir 'ne_110m_admin_0_countries.geojson'
$OutFile = Join-Path $RepoRoot 'common/webui/assets/worldmap.js'

# 中国省级落点，键为去掉行政后缀后的简称，值与归属地库的省份文本按前缀对齐
$ProvincePoints = [ordered]@{
    '北京' = @(116.4, 39.9); '天津' = @(117.2, 39.1); '河北' = @(114.5, 38.0); '山西' = @(112.5, 37.9)
    '内蒙古' = @(111.7, 40.8); '辽宁' = @(123.4, 41.8); '吉林' = @(125.3, 43.9); '黑龙江' = @(126.6, 45.8)
    '上海' = @(121.5, 31.2); '江苏' = @(119.0, 32.9); '浙江' = @(120.2, 29.2); '安徽' = @(117.3, 31.9)
    '福建' = @(118.3, 26.1); '江西' = @(115.9, 27.7); '山东' = @(117.0, 36.7); '河南' = @(113.6, 34.8)
    '湖北' = @(114.3, 30.6); '湖南' = @(111.7, 27.6); '广东' = @(113.3, 23.1); '广西' = @(108.3, 22.8)
    '海南' = @(109.8, 19.2); '重庆' = @(107.9, 30.0); '四川' = @(102.7, 30.7); '贵州' = @(106.7, 26.6)
    '云南' = @(101.5, 25.0); '西藏' = @(88.0, 31.0); '陕西' = @(108.9, 35.4); '甘肃' = @(103.8, 37.0)
    '青海' = @(95.9, 35.6); '宁夏' = @(106.2, 37.3); '新疆' = @(85.0, 41.0); '台湾' = @(121.0, 23.7)
    '香港' = @(114.2, 22.3); '澳门' = @(113.5, 22.2)
}

# 南极在裁剪范围内会被压成一条横向色带，直接跳过更干净
$SkippedCountries = @('Antarctica')

# 轮廓数据里的官方国名与归属地库文本不一致的，按归属地库的写法再收录一份别名
$CountryAliases = @{
    'United States of America' = @('United States')
    'Czechia'                  = @('Czech Republic')
    'North Korea'              = @('Korea')
    'South Korea'              = @('Korea')
    'Russia'                   = @('Russian Federation')
    'Syria'                    = @('Syrian Arab Republic')
    'Laos'                     = @('Lao People''s Democratic Republic')
    'Moldova'                  = @('Republic of Moldova')
    'North Macedonia'          = @('Macedonia')
    'Vietnam'                  = @('Viet Nam')
    'Turkey'                   = @('Turkiye')
    'Iran'                     = @('Iran (Islamic Republic of)')
}

# 墨卡托纵坐标，纬度先夹到裁剪范围内再换算
function ConvertTo-Mercator([double]$latitude) {
    $clamped = [Math]::Max([Math]::Min($latitude, 89), -89)
    $radians = $clamped * [Math]::PI / 180
    return [Math]::Log([Math]::Tan([Math]::PI / 4 + $radians / 2))
}

$topY = ConvertTo-Mercator $TopLatitude
$bottomY = ConvertTo-Mercator $BottomLatitude
$scale = $Width / (2 * [Math]::PI)
$height = [Math]::Round(($topY - $bottomY) * $scale, 2)

# 经纬度换算成页面坐标，裁剪范围之外的纬度由墨卡托函数夹住
function ConvertTo-Point([double]$longitude, [double]$latitude) {
    $x = ($longitude + 180) / 360 * $Width
    $y = ($topY - (ConvertTo-Mercator $latitude)) * $scale
    return @([Math]::Round($x, $Precision), [Math]::Round($y, $Precision))
}

if (-not (Test-Path -LiteralPath $ScratchDir -PathType Container)) {
    New-Item -ItemType Directory -Path $ScratchDir -Force | Out-Null
}
if (-not (Test-Path -LiteralPath $SourceFile -PathType Leaf)) {
    Write-Host "[map] 下载轮廓数据: $SourceUrl" -ForegroundColor Cyan
    $ProgressPreference = 'SilentlyContinue'
    $response = Invoke-WebRequest -Uri $SourceUrl -UseBasicParsing -TimeoutSec 120
    Set-Content -LiteralPath $SourceFile -Value $response.Content -Encoding utf8
}

$geo = Get-Content -LiteralPath $SourceFile -Raw -Encoding utf8 | ConvertFrom-Json
Write-Host "[map] 国家数量: $($geo.features.Count)" -ForegroundColor Cyan

# 把一整个多边形环拼成子路径，坐标超出裁剪范围的纬度已经在投影里夹住
function ConvertTo-RingPath($ring) {
    $builder = [System.Text.StringBuilder]::new()
    $first = $true
    foreach ($point in $ring) {
        $pair = ConvertTo-Point $point[0] $point[1]
        if ($first) {
            [void]$builder.Append('M').Append($pair[0]).Append(',').Append($pair[1])
            $first = $false
        }
        else {
            [void]$builder.Append('L').Append($pair[0]).Append(',').Append($pair[1])
        }
    }
    [void]$builder.Append('Z')
    return $builder.ToString()
}

$pathBuilder = [System.Text.StringBuilder]::new()
$places = [ordered]@{}
$rings = 0
foreach ($feature in $geo.features) {
    $name = $feature.properties.NAME
    $nameZh = $feature.properties.NAME_ZH
    if ($SkippedCountries -contains $name) { continue }

    $geometry = $feature.geometry
    if ($null -eq $geometry) { continue }
    $polygons = @()
    if ($geometry.type -eq 'Polygon') {
        $polygons = @(, $geometry.coordinates)
    }
    elseif ($geometry.type -eq 'MultiPolygon') {
        $polygons = $geometry.coordinates
    }
    foreach ($polygon in $polygons) {
        foreach ($ring in $polygon) {
            if ($ring.Count -lt 4) { continue }
            [void]$pathBuilder.Append((ConvertTo-RingPath $ring))
            $rings += 1
        }
    }

    $labelX = $feature.properties.LABEL_X
    $labelY = $feature.properties.LABEL_Y
    if ($null -eq $labelX -or $null -eq $labelY) { continue }
    $point = ConvertTo-Point $labelX $labelY
    # 英文名、中文名与别名同时收录，归属地库的各种写法都能落点
    $keys = @($name)
    if ($CountryAliases.ContainsKey($name)) { $keys += $CountryAliases[$name] }
    foreach ($key in @($keys + $nameZh)) {
        if ([string]::IsNullOrWhiteSpace($key)) { continue }
        $places[$key] = $point
    }
}

foreach ($province in $ProvincePoints.Keys) {
    $coordinate = $ProvincePoints[$province]
    $places[$province] = ConvertTo-Point $coordinate[0] $coordinate[1]
}

Write-Host "[map] 环数量: $rings，落点数量: $($places.Count)" -ForegroundColor Cyan

$svg = '<svg class="map-svg" viewBox="0 0 {0} {1}" preserveAspectRatio="xMidYMid meet" role="img">' -f $Width, $height
$svg += '<path class="map-land" fill-rule="evenodd" d="' + $pathBuilder.ToString() + '"/></svg>'

$placeLines = foreach ($key in $places.Keys) {
    $point = $places[$key]
    '  {0}: [{1}, {2}]' -f ($key | ConvertTo-Json -Compress), $point[0], $point[1]
}

# 省级落点单独列出键，页面先按省级匹配再退回国家级，避免国名把省份盖掉
$provinceLines = ($ProvincePoints.Keys | ForEach-Object { '  {0}' -f ($_ | ConvertTo-Json -Compress) }) -join ",`n"

$content = @"
// worldmap 是服务端展示页内嵌的世界底图，由 platforms/make-map.ps1 生成
//
// 底图来自 Natural Earth 110m 国家轮廓（公有领域），构建期换算成墨卡托投影的 SVG 路径，
// 落点键与归属地库的国家名、中国省级简称对齐，页面运行时不联网
var WORLD_MAP = {
  width: $Width,
  height: $height,
  svg: '$($svg -replace "'", "\'")',
  provinces: [
$provinceLines
  ],
  places: {
$($placeLines -join ",`n")
  }
};
"@

Set-Content -LiteralPath $OutFile -Value $content -Encoding utf8
$size = (Get-Item -LiteralPath $OutFile).Length
Write-Host "[map] 已写出 $OutFile（$size 字节）" -ForegroundColor Green