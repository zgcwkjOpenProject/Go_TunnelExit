#Requires -Version 7.0
<#
.SYNOPSIS
    生成各平台套件图标与桌面图标

.DESCRIPTION
    先用 GDI+ 画出一枚透明底的「隧道」图形，再由 FFmpeg 合成圆角方形底板并缩放写出全部尺寸，
    使各平台图标呈现 app 图标那样的圆角方形外观。
    套件图标（飞牛的 ICON.PNG、群晖的 PACKAGE_ICON.PNG）与 Android 应用图标保持 PNG，
    桌面图标走网页入口：飞牛与群晖都只按 icon_{0}.png 取图，因而统一用 PNG。
    全部输出都保留 alpha 通道。仓库提交的是生成结果，构建脚本不做缩放；
    调整底板配色或图形占比后重新执行本脚本即可。

.PARAMETER BackgroundColor
    底板纯色，取值形如 #RRGGBB

.PARAMETER GlyphScale
    图形宽度占主图宽度的比例

.PARAMETER CornerRatio
    圆角半径占主图宽度的比例

.EXAMPLE
    ./platforms/make-icons.ps1
    使用脚本内置的默认底板重新生成全部图标
#>
[CmdletBinding()]
param(
    [string]$BackgroundColor = '#1F6FEB',
    [double]$GlyphScale = 0.86,
    [double]$CornerRatio = 0.22
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# 图形绘制在固定尺寸的画布上，缩放交给 FFmpeg，保证各尺寸笔画比例一致
$GlyphCanvas = 1024
# 笔画宽度与两层拱门的几何参数，取值均为 1024 画布下的像素
$GlyphStroke = 88
$OuterArch = @{ X = 140; Y = 140; Size = 744; Bottom = 860 }
$InnerArch = @{ X = 310; Y = 310; Size = 404; Bottom = 860 }

$PlatformRoot = $PSScriptRoot
$RepoRoot = Split-Path -Parent $PlatformRoot
$ScratchRoot = Join-Path $RepoRoot 'build/icon-scratch'

foreach ($tool in @('ffmpeg', 'ffprobe')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        throw "未找到 $tool，请先安装 FFmpeg 并加入 PATH"
    }
}

# GDI+ 在不同 .NET 版本下位于不同程序集名，逐个尝试加载
function Initialize-Drawing {
    foreach ($assembly in @('System.Drawing.Common', 'System.Drawing')) {
        try {
            Add-Type -AssemblyName $assembly -ErrorAction Stop
            return
        }
        catch {
            continue
        }
    }
    throw '无法加载 System.Drawing，图标生成中止'
}

# 执行 ffmpeg，失败时抛出异常而不是留下半成品文件
function Invoke-Ffmpeg {
    param([string[]]$Arguments)

    & ffmpeg -hide_banner -y -loglevel error @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "ffmpeg 执行失败：ffmpeg $($Arguments -join ' ')"
    }
}

# 画出隧道图形：两层拱门表示「端口贯通」，透明底、纯白描边
function New-GlyphImage {
    param([string]$OutputPath)

    $bitmap = [System.Drawing.Bitmap]::new($GlyphCanvas, $GlyphCanvas, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    try {
        $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
        try {
            $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
            $graphics.Clear([System.Drawing.Color]::Transparent)

            foreach ($arch in @($OuterArch, $InnerArch)) {
                $path = [System.Drawing.Drawing2D.GraphicsPath]::new()
                try {
                    # 路径严格按笔画顺序连续添加：左腿自下向上、圆弧扫过 180 度到右侧中点、右腿向下，
                    # 跳过任一段都会让 GDI+ 用直线连接断点，在拱门内部留下斜向白块
                    $right = $arch.X + $arch.Size
                    $middle = $arch.Y + $arch.Size / 2
                    $path.AddLine($arch.X, $arch.Bottom, $arch.X, $middle)
                    $path.AddArc($arch.X, $arch.Y, $arch.Size, $arch.Size, 180, 180)
                    $path.AddLine($right, $middle, $right, $arch.Bottom)

                    $pen = [System.Drawing.Pen]::new([System.Drawing.Color]::White, $GlyphStroke)
                    try {
                        $pen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
                        $pen.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
                        $pen.LineJoin = [System.Drawing.Drawing2D.LineJoin]::Round
                        $graphics.DrawPath($pen, $path)
                    }
                    finally {
                        $pen.Dispose()
                    }
                }
                finally {
                    $path.Dispose()
                }
            }
        }
        finally {
            $graphics.Dispose()
        }

        $directory = Split-Path -Parent $OutputPath
        if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
            New-Item -ItemType Directory -Path $directory -Force | Out-Null
        }
        $bitmap.Save($OutputPath, [System.Drawing.Imaging.ImageFormat]::Png)
    }
    finally {
        $bitmap.Dispose()
    }
}

# 读取源图边长，源图必须是正方形
function Get-SourceSize {
    param([string]$Path)

    $sizeText = & ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 $Path
    $parts = $sizeText.Trim() -split ','
    if ($parts.Count -ne 2 -or [int]$parts[0] -ne [int]$parts[1]) {
        throw "源图必须是正方形：$sizeText"
    }
    return [int]$parts[0]
}

# 用距离场表达式生成圆角方形纯色底板，圆角外保持透明
function New-RoundedBackground {
    param(
        [int]$Size,
        [int]$Radius,
        [string]$Color,
        [string]$OutputPath
    )

    $half = $Size / 2
    $inner = $half - $Radius
    $dx = "max(0\,abs(X-$half)-$inner)"
    $dy = "max(0\,abs(Y-$half)-$inner)"
    $alpha = "255*clip($Radius-sqrt(pow($dx\,2)+pow($dy\,2))+0.5\,0\,1)"
    $filter = "format=rgba,geq=r='r(X,Y)':g='g(X,Y)':b='b(X,Y)':a='$alpha'"

    Invoke-Ffmpeg @(
        '-f', 'lavfi',
        '-i', "color=c=${Color}:s=${Size}x${Size}",
        '-vf', $filter,
        '-frames:v', '1',
        $OutputPath
    )
}

# 把图形按目标占比缩放后居中叠到底板上，得到统一的主图
function New-MasterImage {
    param(
        [string]$BackgroundPath,
        [string]$GlyphPath,
        [int]$CanvasSize,
        [int]$GlyphWidth,
        [int]$GlyphHeight,
        [int]$GlyphX,
        [int]$GlyphY,
        [double]$Scale,
        [string]$OutputPath
    )

    $scaledWidth = [int][Math]::Round($GlyphWidth * $Scale / 2) * 2
    $scaledHeight = [int][Math]::Round($GlyphHeight * $Scale / 2) * 2
    $filter = "[1:v]crop=${GlyphWidth}:${GlyphHeight}:${GlyphX}:${GlyphY},scale=${scaledWidth}:${scaledHeight}:flags=lanczos[glyph];" +
    "[0:v][glyph]overlay=x=(W-w)/2:y=(H-h)/2:format=auto"

    Invoke-Ffmpeg @(
        '-i', $BackgroundPath,
        '-i', $GlyphPath,
        '-filter_complex', $filter,
        '-frames:v', '1',
        $OutputPath
    )
}

# 由主图缩放写出单个图标文件
function Write-Icon {
    param(
        [string]$MasterPath,
        [string]$TargetPath,
        [int]$Size
    )

    $directory = Split-Path -Parent $TargetPath
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }

    $arguments = @(
        '-i', $MasterPath,
        '-vf', "scale=${Size}:${Size}:flags=lanczos",
        '-frames:v', '1',
        $TargetPath
    )

    Invoke-Ffmpeg $arguments
    Write-Host ("  {0,-34} {1}x{1}" -f (Split-Path -Leaf $TargetPath), $Size) -ForegroundColor DarkGray
}

# 套件图标与 Android 应用图标清单，两者都保持 PNG
$PackageIcons = @(
    @{ Path = 'fnos/ICON.PNG'; Size = 512 },
    @{ Path = 'fnos/ICON_256.PNG'; Size = 256 },
    @{ Path = 'synology/PACKAGE_ICON.PNG'; Size = 512 },
    @{ Path = 'synology/PACKAGE_ICON_256.PNG'; Size = 256 },
    @{ Path = 'android/app/src/main/res/drawable/ic_tunnelexit.png'; Size = 512 }
)

# 桌面图标实际尺寸相对清单尺寸的倍数，仅在有网页入口的平台才需要
$DesktopIconScale = 2
$DesktopSizes = @(16, 24, 32, 48, 64, 72, 128, 256)
# 各平台桌面图标的输出目录：飞牛两个角色都有桌面入口，群晖只有客户端套件有
$DesktopIconTargets = @(
    @{ Directory = 'fnos/client/ui/images'; Extension = 'png' },
    @{ Directory = 'fnos/server/ui/images'; Extension = 'png' },
    @{ Directory = 'synology/client/package/ui/images'; Extension = 'png' }
)

Initialize-Drawing

$colorValue = '0x' + $BackgroundColor.TrimStart('#').ToUpperInvariant()

if (Test-Path -LiteralPath $ScratchRoot) {
    Remove-Item -LiteralPath $ScratchRoot -Recurse -Force
}
New-Item -ItemType Directory -Path $ScratchRoot -Force | Out-Null

try {
    $glyphPath = Join-Path $ScratchRoot 'glyph.png'
    New-GlyphImage -OutputPath $glyphPath

    $canvasSize = Get-SourceSize -Path $glyphPath
    $radius = [int][Math]::Round($canvasSize * $CornerRatio)
    $scale = $GlyphScale

    Write-Host ''
    Write-Host '============================================================' -ForegroundColor Cyan
    Write-Host '   生成带圆角底板的平台图标' -ForegroundColor Cyan
    Write-Host '============================================================' -ForegroundColor Cyan
    Write-Host "   画布:   ${canvasSize}x${canvasSize}，圆角半径 $radius"
    Write-Host "   图形:   占比 $GlyphScale"
    Write-Host "   底板:   $BackgroundColor"
    Write-Host ''

    $backgroundPath = Join-Path $ScratchRoot 'background.png'
    $masterPath = Join-Path $ScratchRoot 'master.png'

    New-RoundedBackground -Size $canvasSize -Radius $radius -Color $colorValue -OutputPath $backgroundPath
    New-MasterImage -BackgroundPath $backgroundPath -GlyphPath $glyphPath `
        -CanvasSize $canvasSize -GlyphWidth $canvasSize -GlyphHeight $canvasSize -GlyphX 0 -GlyphY 0 `
        -Scale $scale -OutputPath $masterPath

    Write-Host '  [套件图标]' -ForegroundColor Green
    foreach ($icon in $PackageIcons) {
        Write-Icon -MasterPath $masterPath -TargetPath (Join-Path $PlatformRoot $icon.Path) -Size $icon.Size
    }

    if ($DesktopIconTargets.Count -gt 0) {
        Write-Host ''
        Write-Host '  [桌面图标]' -ForegroundColor Green
        foreach ($target in $DesktopIconTargets) {
            foreach ($size in $DesktopSizes) {
                $targetPath = Join-Path $PlatformRoot (Join-Path $target.Directory "icon_${size}.$($target.Extension)")
                Write-Icon -MasterPath $masterPath -TargetPath $targetPath -Size ($size * $DesktopIconScale)
            }
        }
    }
}
finally {
    if (Test-Path -LiteralPath $ScratchRoot) {
        Remove-Item -LiteralPath $ScratchRoot -Recurse -Force
    }
}

Write-Host ''
Write-Host '  全部图标已生成' -ForegroundColor Green
Write-Host '============================================================' -ForegroundColor Cyan
Write-Host ''
