# 平台打包脚本共用函数
#
# 本文件被 platforms/build-*.ps1 通过点源方式引入，只提供函数，不执行任何动作

# 交叉编译 TunnelExit 二进制
# 临时设置 GOOS/GOARCH/CGO_ENABLED，编译后恢复，避免污染调用方环境
function Invoke-TunnelExitBuild {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$SourceDir,
        [Parameter(Mandatory)][string]$OutPath,
        [Parameter(Mandatory)][string]$Package,
        [string]$GOOS = 'linux',
        [string]$GOARCH = 'amd64',
        [string]$GOARM = '',
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][string]$Platform,
        [string]$BuildTime = (Get-Date -Format 'yyyy-MM-ddTHH:mm:ss'),
        [switch]$EnableCGO,
        [string]$CCompiler = ''
    )
    if (-not (Test-Path (Join-Path $SourceDir 'go.mod'))) {
        throw "Go 模块目录缺少 go.mod: $SourceDir"
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw '未找到 go 工具链，请先安装 Go 并加入 PATH'
    }
    if ($EnableCGO -and $CCompiler -and -not (Test-Path $CCompiler)) {
        throw "未找到 C 编译器: $CCompiler"
    }

    # 保存原值，编译后恢复（$null 表示原本未设置，用完删除）
    $previous = @{
        GOOS        = [System.Environment]::GetEnvironmentVariable('GOOS')
        GOARCH      = [System.Environment]::GetEnvironmentVariable('GOARCH')
        GOARM       = [System.Environment]::GetEnvironmentVariable('GOARM')
        CGO_ENABLED = [System.Environment]::GetEnvironmentVariable('CGO_ENABLED')
        CC          = [System.Environment]::GetEnvironmentVariable('CC')
    }
    $outDir = Split-Path -Parent $OutPath
    if ($outDir -and -not (Test-Path $outDir)) {
        New-Item -ItemType Directory -Path $outDir -Force | Out-Null
    }
    try {
        $env:GOOS = $GOOS
        $env:GOARCH = $GOARCH
        if ($GOARM) { $env:GOARM = $GOARM }
        # Android 上必须用 bionic 的 getaddrinfo 才能解析域名，因此允许调用方开启 CGO
        if ($EnableCGO) {
            $env:CGO_ENABLED = '1'
            if ($CCompiler) { $env:CC = $CCompiler }
        }
        else {
            $env:CGO_ENABLED = '0'
        }
        # BuildTime 使用不含空格的 ISO 形式，避免 ldflags 被空格切断
        $ldflags = "-s -w -X tunnelexit/app.BuildVersion=$Version -X tunnelexit/app.BuildTime=$BuildTime -X tunnelexit/app.BuildPlatform=$Platform"
        & go build -C $SourceDir -trimpath -ldflags $ldflags -o $OutPath $Package
        if ($LASTEXITCODE -ne 0) { throw "go build 失败，退出码 $LASTEXITCODE" }
    }
    finally {
        foreach ($key in $previous.Keys) {
            if ($null -eq $previous[$key]) { Remove-Item "Env:$key" -ErrorAction SilentlyContinue }
            else { Set-Item "Env:$key" $previous[$key] }
        }
    }
    if (-not (Test-Path $OutPath)) { throw "go build 未产出二进制: $OutPath" }
    $size = [Math]::Round((Get-Item $OutPath).Length / 1MB, 2)
    Write-Host "[go] 编译完成: $(Split-Path -Leaf $OutPath) ($GOOS/$GOARCH, $size MB)" -ForegroundColor Green
}

# 写入服务端默认配置，字段与 app.ServerConfig 保持一致
#
# 界面字段一并写出，用户能看到展示页的监听地址、端口与挂载路径前缀该怎么填
function Write-TunnelExitServerConfig {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [int]$Port = 9733,
        [string]$SocksHost = '0.0.0.0',
        [int]$SocksPortBase = 19733,
        [int]$SocksPortCount = 2500,
        [int]$PoolSize = 16,
        [int]$NodeKeepDays = 0,
        [string]$WebHost = '127.0.0.1',
        [int]$WebPort = 9732,
        [string]$WebPath = '/'
    )
    $json = @{
        port           = $Port
        socksHost      = $SocksHost
        socksPortBase  = $SocksPortBase
        socksPortCount = $SocksPortCount
        poolSize       = $PoolSize
        nodeKeepDays   = $NodeKeepDays
        webHost        = $WebHost
        webPort        = $WebPort
        webPath        = $WebPath
    } | ConvertTo-Json -Compress
    [System.IO.File]::WriteAllText($Path, $json, [System.Text.UTF8Encoding]::new($false))
}

# 写入客户端默认配置，字段与 app.ClientConfig 保持一致
#
# 客户端唯一 ID 由程序首次运行时生成并写回，因此这里不预置 id 字段；
# 服务端公钥同样留空，由用户在客户端配置页粘贴服务端 keys/public_key.pem 里的 PEM 文本
function Write-TunnelExitClientConfig {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [string]$ServerHost = '',
        [int]$Port = 9733,
        [string]$ServerKey = '',
        [string]$Name = '',
        [int]$PoolSize = 16,
        [string]$WebHost = '127.0.0.1',
        [int]$WebPort = 7003,
        [string]$WebPath = '/'
    )
    $json = @{
        serverHost = $ServerHost
        port       = $Port
        serverKey  = $ServerKey
        name       = $Name
        poolSize   = $PoolSize
        webHost    = $WebHost
        webPort    = $WebPort
        webPath    = $WebPath
    } | ConvertTo-Json -Compress
    [System.IO.File]::WriteAllText($Path, $json, [System.Text.UTF8Encoding]::new($false))
}

# 生成发布产物文件名：服务端用 tunnelexit- 前缀，客户端用 tunnelexit-client- 前缀区分角色
function Get-TunnelExitArtifactName {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][ValidateSet('server', 'client')][string]$Role,
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][string]$Flavor,
        [Parameter(Mandatory)][string]$Arch,
        [Parameter(Mandatory)][string]$Extension
    )
    $prefix = if ($Role -eq 'server') { 'tunnelexit' } else { 'tunnelexit-client' }
    return "$prefix-$Version-$Flavor-$Arch.$Extension"
}

# 复制离线归属地库及其许可协议到打包目录
#
# IPv4 与 IPv6 各一个库，两个文件加起来约 47 MB，flash 紧张的设备可设置 TUNNELEXIT_SKIP_GEOIP=1 全部跳过，
# 只想省掉 IPv6 库时用 TUNNELEXIT_SKIP_GEOIP_V6=1，留下 IPv4 库
# 跳过或库文件缺失时只提示不中断，服务端运行起来会把对应地址族的归属地显示为未知地区
function Copy-TunnelExitGeoIP {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$RepoRoot,
        [Parameter(Mandatory)][string]$DestDir
    )
    if ($env:TUNNELEXIT_SKIP_GEOIP -eq '1') {
        Write-Host '[geoip] 已按 TUNNELEXIT_SKIP_GEOIP=1 跳过归属地库' -ForegroundColor Yellow
        return
    }
    $names = @('ip2region_v4.xdb')
    if ($env:TUNNELEXIT_SKIP_GEOIP_V6 -eq '1') {
        Write-Host '[geoip] 已按 TUNNELEXIT_SKIP_GEOIP_V6=1 只附带 IPv4 归属地库' -ForegroundColor Yellow
    } else {
        $names += 'ip2region_v6.xdb'
    }
    $assetsDir = Join-Path $RepoRoot 'assets'
    New-Item -ItemType Directory -Path $DestDir -Force | Out-Null
    $total = 0
    foreach ($name in $names) {
        $source = Join-Path $assetsDir $name
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            Write-Warning "缺少归属地库 ${source}，该地址族的来源会显示未知地区"
            continue
        }
        Copy-Item -LiteralPath $source -Destination (Join-Path $DestDir $name) -Force
        $total += (Get-Item -LiteralPath $source).Length
    }
    $license = Join-Path $assetsDir 'ip2region.LICENSE.md'
    if (Test-Path -LiteralPath $license -PathType Leaf) {
        Copy-Item -LiteralPath $license -Destination (Join-Path $DestDir 'ip2region.LICENSE.md') -Force
    }
    if ($total -gt 0) {
        $size = [Math]::Round($total / 1MB, 2)
        Write-Host "[geoip] 已附带归属地库 $($names -join '、')，共 $size MB" -ForegroundColor Green
    }
}

# 读取仓库唯一版本号
function Get-TunnelExitVersion {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$VersionFile)
    if (-not (Test-Path -LiteralPath $VersionFile -PathType Leaf)) {
        throw '无法确定版本号：缺少 common/version'
    }
    $version = (Get-Content -LiteralPath $VersionFile -Raw).Trim()
    if ([string]::IsNullOrWhiteSpace($version)) {
        throw '无法确定版本号：common/version 为空'
    }
    return $version
}

# 安全清理仓库内的暂存目录，拒绝删除 build/ 之外的路径
function Remove-TunnelExitStage {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$BuildRoot
    )
    if (-not (Test-Path $Path)) { return }
    $resolved = (Resolve-Path $Path).Path
    $buildResolved = (Resolve-Path $BuildRoot).Path
    $prefix = $buildResolved + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "拒绝删除预期之外的路径：$resolved"
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}

# 向 tar 写入目录并指定 Unix 权限
function Add-TarDirectory {
    param(
        [System.Formats.Tar.TarWriter]$Writer,
        [string]$Name
    )
    $entry = [System.Formats.Tar.PaxTarEntry]::new(
        [System.Formats.Tar.TarEntryType]::Directory,
        ($Name.TrimEnd('/') + '/')
    )
    $entry.Mode = [System.IO.UnixFileMode]([Convert]::ToInt32('755', 8))
    $Writer.WriteEntry($entry)
}

# 写入单个文件到 tar，并显式指定 Unix 权限位
function Add-TarFile {
    param(
        [System.Formats.Tar.TarWriter]$Writer,
        [string]$Source,
        [string]$Name,
        [string]$ModeOctal = '644'
    )
    $mode = [System.IO.UnixFileMode]([Convert]::ToInt32($ModeOctal, 8))
    $stream = [System.IO.File]::OpenRead($Source)
    try {
        $entry = [System.Formats.Tar.PaxTarEntry]::new(
            [System.Formats.Tar.TarEntryType]::RegularFile,
            ($Name -replace '\\', '/')
        )
        $entry.Mode = $mode
        $entry.DataStream = $stream
        $Writer.WriteEntry($entry)
    }
    finally {
        $stream.Dispose()
    }
}

# 递归写入目录树到 tar，并按 ExecSet 标记可执行文件
function Add-Tree {
    param(
        [System.Formats.Tar.TarWriter]$Writer,
        [string]$Root,
        [string]$Prefix = '',
        [hashtable]$ExecSet = @{ }
    )
    Get-ChildItem -LiteralPath $Root -Force | ForEach-Object {
        $rel = if ($Prefix) { "$Prefix/$($_.Name)" } else { $_.Name }
        if ($_.PSIsContainer) {
            Add-TarDirectory -Writer $Writer -Name $rel
            Add-Tree -Writer $Writer -Root $_.FullName -Prefix $rel -ExecSet $ExecSet
        }
        else {
            $mode = if ($ExecSet.ContainsKey($rel)) { '755' } else { '644' }
            Add-TarFile -Writer $Writer -Source $_.FullName -Name $rel -ModeOctal $mode
        }
    }
}

# 生成 gzip 压缩的 tar 包，用于群晖与 OpenWrt 等需要 tar.gz 的场景
function New-TarGz {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$SourceDir,
        [Parameter(Mandatory)][string]$OutPath,
        [hashtable]$ExecSet = @{ }
    )
    if (Test-Path $OutPath) { Remove-Item -LiteralPath $OutPath -Force }
    $outDir = Split-Path -Parent $OutPath
    if ($outDir -and -not (Test-Path $outDir)) {
        New-Item -ItemType Directory -Path $outDir -Force | Out-Null
    }
    $fileStream = [System.IO.File]::Create($OutPath)
    try {
        $gzipStream = [System.IO.Compression.GZipStream]::new($fileStream, [System.IO.Compression.CompressionLevel]::Optimal, $true)
        try {
            $writer = [System.Formats.Tar.TarWriter]::new($gzipStream, $false)
            try {
                Add-Tree -Writer $writer -Root $SourceDir -Prefix '' -ExecSet $ExecSet
            }
            finally {
                $writer.Dispose()
            }
        }
        finally {
            $gzipStream.Dispose()
        }
    }
    finally {
        $fileStream.Dispose()
    }
    if (-not (Test-Path $OutPath)) { throw "tar.gz 未生成：$OutPath" }
}

# 去除 JS 注释（跳过字符串与正则字面量，避免误删 URL 等）
function Remove-JsComments([string]$Text) {
    $builder = [System.Text.StringBuilder]::new()
    $index = 0
    $length = $Text.Length
    $state = 'code'
    $lastCodeChar = [char]0
    $regexClass = $false
    while ($index -lt $length) {
        $char = $Text[$index]
        $next = if ($index + 1 -lt $length) { $Text[$index + 1] } else { '' }
        switch ($state) {
            'line' {
                if ($char -eq "`n" -or $char -eq "`r") { $state = 'code'; [void]$builder.Append($char) }
                $index++
            }
            'block' {
                if ($char -eq '*' -and $next -eq '/') { $state = 'code'; $index += 2 }
                else { $index++ }
            }
            'sq' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq "'") { $state = 'code'; $index++ }
                else { $index++ }
            }
            'dq' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq '"') { $state = 'code'; $index++ }
                else { $index++ }
            }
            'tpl' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq '`') { $state = 'code'; $lastCodeChar = $char; $index++ }
                else { $index++ }
            }
            'regex' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq '[') { $regexClass = $true; $index++ }
                elseif ($char -eq ']') { $regexClass = $false; $index++ }
                elseif ($char -eq '/' -and -not $regexClass) { $state = 'code'; $lastCodeChar = $char; $index++ }
                else { $index++ }
            }
            default {
                if ($char -eq '/' -and $next -eq '/') { $state = 'line'; $index += 2 }
                elseif ($char -eq '/' -and $next -eq '*') { $state = 'block'; $index += 2 }
                elseif ($char -eq "'") { $state = 'sq'; [void]$builder.Append($char); $index++ }
                elseif ($char -eq '"') { $state = 'dq'; [void]$builder.Append($char); $index++ }
                elseif ($char -eq '`') { $state = 'tpl'; [void]$builder.Append($char); $index++ }
                elseif ($char -eq '/' -and (Test-JsRegexStart $lastCodeChar)) {
                    $state = 'regex'
                    $regexClass = $false
                    [void]$builder.Append($char)
                    $index++
                }
                else {
                    [void]$builder.Append($char)
                    if (-not [char]::IsWhiteSpace($char)) { $lastCodeChar = $char }
                    $index++
                }
            }
        }
    }
    return $builder.ToString()
}

# 判断折叠空白时两个字符之间是否必须保留一个分隔空格
function Test-JsSpaceNeeded([char]$Left, [char]$Right) {
    $l = [string]$Left
    $r = [string]$Right
    return ($l -match '[A-Za-z0-9_$]' -and $r -match '[A-Za-z0-9_$]')
}

# 判断当前位置的斜杠是否更可能是正则字面量的起点
function Test-JsRegexStart([char]$Left) {
    if ($Left -eq [char]0) { return $true }
    return '([{=,:;!&|?+-*~^<>'.Contains([string]$Left)
}

# 判断上一行是否是 if、for 等控制语句的条件，避免压缩时错误插入分号
function Test-JsControlCondition([System.Text.StringBuilder]$Text) {
    if ($Text.Length -eq 0 -or $Text[$Text.Length - 1] -ne ')') { return $false }
    $depth = 0
    for ($index = $Text.Length - 1; $index -ge 0; $index--) {
        if ($Text[$index] -eq ')') { $depth++ }
        elseif ($Text[$index] -eq '(') {
            $depth--
            if ($depth -ne 0) { continue }
            $before = $index - 1
            while ($before -ge 0 -and [char]::IsWhiteSpace($Text[$before])) { $before-- }
            $end = $before
            while ($before -ge 0 -and [char]::IsLetter($Text[$before])) { $before-- }
            $keyword = $Text.ToString($before + 1, $end - $before)
            return $keyword -in @('if', 'for', 'while', 'switch')
        }
    }
    return $false
}

# 判断压缩时换行后的字符是否会与前一条语句发生语义粘连
function Test-JsLineBreakNeedsSemicolon([System.Text.StringBuilder]$Text, [char]$Right) {
    if ($Text.Length -eq 0) { return $false }
    if (Test-JsControlCondition $Text) { return $false }
    $left = $Text[$Text.Length - 1]
    $canEndStatement = [char]::IsLetterOrDigit($left) -or '_$)]}'.Contains([string]$left) -or $left -eq "'" -or $left -eq '"' -or $left -eq [char]96
    if (($left -eq '+' -or $left -eq '-') -and $Text.Length -gt 1 -and $Text[$Text.Length - 2] -eq $left) {
        $canEndStatement = $true
    }
    $continuesExpression = ',.;:)]}([+-*/%&|=!<>?'.Contains([string]$Right)
    return $canEndStatement -and -not $continuesExpression
}

# 压缩 JS：去注释并移除代码区换行
function Minify-JsText([string]$Text) {
    $Text = Remove-JsComments $Text
    $builder = [System.Text.StringBuilder]::new()
    $index = 0
    $length = $Text.Length
    $state = 'code'
    $pendingSpace = $false
    $pendingNewline = $false
    $lastCodeChar = [char]0
    $regexClass = $false

    while ($index -lt $length) {
        $char = $Text[$index]
        $next = if ($index + 1 -lt $length) { $Text[$index + 1] } else { [char]0 }

        switch ($state) {
            'sq' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq "'") { $state = 'code'; $lastCodeChar = $char; $index++ }
                else { $index++ }
            }
            'dq' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq '"') { $state = 'code'; $lastCodeChar = $char; $index++ }
                else { $index++ }
            }
            'tpl' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq '`') { $state = 'code'; $lastCodeChar = $char; $index++ }
                else { $index++ }
            }
            'regex' {
                [void]$builder.Append($char)
                if ($char -eq '\' -and $next) { [void]$builder.Append($next); $index += 2 }
                elseif ($char -eq '[') { $regexClass = $true; $index++ }
                elseif ($char -eq ']') { $regexClass = $false; $index++ }
                elseif ($char -eq '/' -and -not $regexClass) { $state = 'code'; $lastCodeChar = $char; $index++ }
                else { $index++ }
            }
            default {
                if ($char -eq "`r" -or $char -eq "`n") {
                    $pendingSpace = $false
                    $pendingNewline = $true
                    $index++
                    continue
                }
                if ([char]::IsWhiteSpace($char)) {
                    $pendingSpace = $true
                    $index++
                    continue
                }
                if ($pendingNewline -and (Test-JsLineBreakNeedsSemicolon $builder $char)) {
                    [void]$builder.Append(';')
                }
                elseif ($pendingSpace -and $lastCodeChar -ne [char]0 -and (Test-JsSpaceNeeded $lastCodeChar $char)) {
                    [void]$builder.Append(' ')
                }
                $pendingSpace = $false
                $pendingNewline = $false
                [void]$builder.Append($char)
                if ($char -eq "'") { $state = 'sq' }
                elseif ($char -eq '"') { $state = 'dq' }
                elseif ($char -eq '`') { $state = 'tpl' }
                elseif ($char -eq '/' -and (Test-JsRegexStart $lastCodeChar)) {
                    $state = 'regex'
                    $regexClass = $false
                }
                $lastCodeChar = $char
                $index++
            }
        }
    }
    return ($builder.ToString() -replace "`r", '' -replace "`n", '').Trim()
}

# 压缩 CSS：去注释、折叠空白、清理常见分隔符周围的多余空格
# 分隔符不含 + 和 -：calc() 要求加减号两侧保留空格，折叠后整条声明会失效
function Minify-CssText([string]$Text) {
    $Text = [regex]::Replace($Text, '(?s)/\*.*?\*/', '')
    $Text = [regex]::Replace($Text, '\s+', ' ')
    $Text = [regex]::Replace($Text, '\s*([{}:;,>~])\s*', '$1')
    $Text = [regex]::Replace($Text, ';}', '}')
    return $Text.Trim()
}

# 压缩 HTML：去注释、折叠标签间空白
function Minify-HtmlText([string]$Text) {
    $Text = [regex]::Replace($Text, '(?s)<!--.*?-->', '')
    $Text = [regex]::Replace($Text, '>\s+<', '><')
    $Text = [regex]::Replace($Text, '\s{2,}', ' ')
    return $Text.Trim()
}

# 处理单个文本资源，按后缀决定是否压缩
function Optimize-TextAsset([string]$Path) {
    if (-not (Test-Path $Path)) { return }
    $extension = [System.IO.Path]::GetExtension($Path).ToLowerInvariant()
    $content = [System.IO.File]::ReadAllText($Path)
    $next = $null
    switch ($extension) {
        '.js' { $next = Minify-JsText $content }
        '.css' { $next = Minify-CssText $content }
        '.html' { $next = Minify-HtmlText $content }
        default { return }
    }
    if ($null -ne $next) {
        [System.IO.File]::WriteAllText($Path, $next, [System.Text.UTF8Encoding]::new($false))
    }
}

# 递归处理目录内的文本资源
function Optimize-TextAssets([string]$Root) {
    if (-not (Test-Path $Root)) { return }
    Get-ChildItem -LiteralPath $Root -Recurse -File | ForEach-Object {
        Optimize-TextAsset $_.FullName
    }
}

# 生成发布说明文本
function Write-TunnelExitReadme {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Title,
        [string[]]$Lines = @()
    )
    # 换行固定用 LF，与包内其他文本资产保持一致
    $content = (@($Title, '') + $Lines) -join "`n"
    [System.IO.File]::WriteAllText($Path, $content, [System.Text.UTF8Encoding]::new($false))
}
