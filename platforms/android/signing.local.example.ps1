#Requires -Version 7.0
<#
.SYNOPSIS
    Android 发布签名凭据模板

.DESCRIPTION
    把本文件复制成同目录下的 signing.local.ps1 并填入自己的 keystore 信息，
    build-android.ps1 会在构建开始时读取它；凭据不进版本库，signing.local.ps1
    已由 .gitignore 忽略。读取不到时构建脚本直接输出未签名 APK
#>

# keystore 绝对路径
$KeystorePath = 'D:\Application\Android\your-release.jks'
# keystore 里的密钥别名
$KeyAlias = 'your-alias'
# keystore 口令
$StorePassword = 'your-store-password'
# 密钥口令
$KeyPassword = 'your-key-password'