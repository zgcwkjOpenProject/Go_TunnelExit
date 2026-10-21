# TunnelExit · Android 客户端

本目录是 Android 客户端外壳（Gradle 工程）。它本身不含任何业务逻辑：
隧道能力全部来自 `common/cmd/client`，构建时交叉编译成
`app/src/main/jniLibs/arm64-v8a/libtunnelexit-client.so` 打进来。

## 为什么是客户端

手机处在移动网络、校园网、公司内网里时，只有它自己能直连到那些资源。
把客户端放在手机上，服务端就能借手机的出口 IP 去访问，这就是整个工具的目标。

## 为什么二进制叫 lib*.so

Android 从 10 开始只允许执行两类文件：

1. 应用的 `nativeLibraryDir`（即 APK 里 `lib/<abi>/` 下的文件）
2. 系统只读分区里的文件

而 `nativeLibraryDir` 只接受 `lib*.so` 命名的文件。所以 Go 编译出的
可执行文件必须改名成 `libtunnelexit-client.so` 放进去，Java 侧再通过
`ProcessBuilder` 启动它。这是刻意为之，不是笔误。

## 为什么必须开 CGO

Go 的纯 Go DNS 解析器直接读 `/etc/resolv.conf`，而 Android 没有这个文件，
域名解析全部走 `netd`。因此必须以 CGO 调用 bionic 的 `getaddrinfo`：

- `CGO_ENABLED=1`
- `CC=<ndk>/toolchains/llvm/prebuilt/windows-x86_64/bin/aarch64-linux-android30-clang.cmd`

构建脚本 `platforms/build-android.ps1` 已经处理好这些。

## 目录结构

```
android/
├── settings.gradle              # 模块声明与依赖仓库
├── build.gradle                 # AGP 插件版本
├── gradle.properties            # JVM 参数、路径检查豁免
├── gradlew / gradlew.bat        # Gradle wrapper
├── gradle/wrapper/              # wrapper 配置与 jar
└── app/
    ├── build.gradle             # namespace、SDK 版本、abiFilters
    └── src/main/
        ├── AndroidManifest.xml  # 权限、Activity、前台服务
        ├── java/com/zgcwkj/tunnelexit/
        │   ├── MainActivity.java     # 主界面：配置、启停、日志
        │   ├── TunnelService.java    # 前台服务：托管客户端进程
        │   ├── TunnelConfig.java     # 配置读写（SharedPreferences → client.json）
        │   └── TunnelLog.java        # 内存日志环形缓冲
        ├── res/drawable/ic_tunnelexit.png
        └── res/values{,-night}/styles.xml
```

`app/src/main/jniLibs/` 由构建脚本生成，已加入 `.gitignore`。

## 运行时数据位置

界面上的"运行目录"是应用私有目录：

```
/data/data/com.zgcwkj.tunnelexit/files/tunnelexit/
├── client.json         # 由 SharedPreferences 渲染而成
└── logs/               # 日志目录
    ├── start.log       # 客户端进程标准输出
    └── tunnelexit.log  # 程序自身日志，按天归档
```

客户端进程通过 `TUNNELEXIT_APP_DIR` 环境变量被告知该目录，无需为 Android
增加任何 Go 侧专用逻辑。

## 界面功能

| 项 | 说明 |
| --- | --- |
| 服务端地址 | 服务端的公网地址，默认 `127.0.0.1` |
| 客户端名称 | 写入服务端日志与节点记录的名称，不影响端口分配，默认 `android` |
| 服务端公钥 | 服务端 `keys/public_key.pem` 里的 PEM 公钥，含首尾行；隧道端口只接受加密连接，未填写或填错都会连不上 |
| 隧道端口 | 固定使用 `9733`，与 Go 侧默认值一致 |
| 客户端 ID | 首次启动生成并保存在设置里，服务端据此与来源 IP 一起稳定分配 SOCKS5 端口 |
| 启动 / 停止 | 前台服务托管 `libtunnelexit-client.so` 进程 |
| 运行日志 | 客户端标准输出的实时尾部（最多 400 行） |

启动后通知栏会有一条常驻通知，服务以 `START_STICKY` 运行，界面退出后
隧道依然保持。

## 构建

```powershell
.\platforms\build-android.ps1
```

Android 只有客户端一个角色，没有 `-Arch` 与 `-Role` 参数

产物：`dist/tunnelexit-client-<版本>-android-arm64.apk`

### 可覆盖的环境变量

| 变量 | 默认值 |
| --- | --- |
| `ANDROID_HOME` | `D:\Application\Android\AndroidSDK` |
| `JAVA_HOME` | `D:\Application\Java\jdk-21.0.2` |

脚本内还可调整 `$NdkVersion`、`$BuildToolsVersion`、`$ApiLevel` 等参数。

### 发布签名

签名凭据不进版本库：把 `signing.local.example.ps1` 复制成
`platforms/android/signing.local.ps1`，填入自己的 keystore 路径与口令即可，
该文件已由 `.gitignore` 忽略。缺失时构建脚本不做签名，直接输出 Gradle
产出的未签名 APK。

## 已知限制

- 只打包 `arm64-v8a`，32 位设备与模拟器（x86_64）不支持。需要时可在
  `app/build.gradle` 的 `abiFilters` 与构建脚本中同时扩展。
- `minSdk 30`（Android 11）。前台服务类型 `dataSync` 从该版本起可用。
