# CFData-WEB v9 → v10 candidate2 重构审计记录（2026-09-18）

## 结论

本次是在已确认的 v9 基础上继续修正 CI，并吸收当前 v2rayNG Real Ping 与 reF1nd testing 实现后形成的 v10 candidate。

最终边界：

- CFData：订阅整理、节点去重、测试调度、3 次真实延迟、固定窗口测速、排序、WebUI。
- reF1nd sing-box / Libbox：代理协议、TLS、DNS、detour、outbound、Provider 等实际网络连接能力。
- Android：通过进程内 Libbox `CommandServer` / `StartedService` 风格运行，不启动 standalone ELF，不依赖 root，不通过 TUN 或本地 mixed proxy 绕行。

## v9 基线确认

当前提供的 CFData-WEB 源码目录带有 commit `03f72a2c6406d865a1485baba65d47cdb96e3f1d` 的完整文件状态；GitHub 对应提交标题为 `v9更新`，并且该提交新增了：

- `singBoxTrueLatencyTest`
- `singBoxTrueSpeedTest`
- `CFDataTrueLatencyTest`
- `CFDataTrueSpeedTest`
- v9 真连接测试文档
- Android API 37 / minor-version SDK 构建调整

因此该源码确认为 v9，而不是凭文件内容猜测版本。

## 当前 reF1nd testing 基线

`reF1nd/sing-box-releases` 的 `dev/testing-build-info.json` 当前指向：

- Version: `1.15.0-alpha.6-reF1nd`
- Source SHA: `9e5ea2101d9dbd4194f877814bb49b3de8c1487b`
- Built At: `2026-09-18T06:13:54Z`
- Build Run: `35312960604`

对应 `reF1nd/sing-box` `reF1nd-testing` 当前 `go.mod` 要求 `go 1.25.5`。

当前 reF1nd 发布工作流实际使用 Go `1.26.8`、JDK 17、Android NDK r28，并从源代码构建 Libbox AAR，再交给 Android 客户端使用。

## v9 → candidate 的 CI 失败根因

原始 Android Action 在构建 Libbox 前固定运行 Go `1.25.4`，而刚拉取的 reF1nd testing 源码要求 Go >= `1.25.5`，且环境设置了 `GOTOOLCHAIN=local`，所以在真正编译前直接退出。

因此这不是 CFData true-test override 先报错，而是 Go toolchain 版本门槛不满足。

## 关键结构变化

### 1. 不再把 Libbox AAR / sing-box 二进制提交到 CFData

仓库只保存两个必要的源码 override：

- `third_party/ref1nd-libbox-overrides/daemon/cfdata_true_test.go`
- `third_party/ref1nd-libbox-overrides/experimental/libbox/cfdata.go`

Android CI 根据当前 testing build metadata 指向的 Source SHA，从 `reF1nd/sing-box` 重新编译 AAR。

`app/libs/libbox.aar` 是构建产物，不进入仓库；standalone sing-box ELF 也不再进入仓库。

### 2. Android 只构建 arm64

CFData 当前 APK 的实际目标是 arm64，因此自建 Libbox CI 只生成 Android arm64 AAR，避免为了 CFData 不使用的 386/arm/amd64 变体消耗构建时间和缓存空间。

这不是削弱 reF1nd 核心能力，而是缩小 CFData 自己的发行产物范围。

### 3. 真连接延迟（candidate 保持 v9 测量语义）

默认 3 次独立 HTTPS 请求，通过指定 sing-box outbound 直接连接：

`outbound -> ResolveDialer -> net/http -> https://speed.cloudflare.com/cdn-cgi/trace`

记录：

- 成功次数 / 丢包率
- 平均、最小、最大真实 TTFB
- outbound dial
- TLS handshake
- 出站 IP
- Cloudflare Colo
- 每次错误

不再使用 TCPing、HTTPing 或旧 TLS 延迟经验倍率。

### 4. 真连接下载

第一阶段完成排序以后，再逐节点进行固定 6 秒连续下载，并严格复用第一阶段实际成功的 outbound。

速度按实际读取字节数 / 实际测量窗口计算 MB/s，不再使用“下载到固定字节数后结束”作为主计时方式。

### 5. Android 可写路径修正

Android `MainActivity` 已把 backend 工作目录与 `CFDATA_DATA_DIR` 指向 app 私有数据目录；CFData 后端的 `cfdata-config.json` 现在统一通过 `CFDATA_DATA_DIR` 定位。

这样可以避免把可写运行时配置误写进 Android native library 目录，从而规避 native library 目录只读导致的权限问题。

## 订阅实现边界

订阅继续采用 reF1nd testing 的 ProviderRemote 能力。CFData 负责生成配置中的 provider 描述和 HTTP client headers；实际下载、解析和缓存由 reF1nd core 负责。

订阅请求头支持 v2rayNG 风格的 `User-Agent`、`Connection`、`Accept-Encoding` 等字段。`http_client` 与旧 `download_detour` 不再混用。

## 配置 schema

`singbox-r-template.json` 已统一指向 `reF1nd-testing/docs/schema.json`，不再指向 `reF1nd-testing-next`，防止测试源和实际运行核心版本错位。

## 清理项

已移除：

- 仓库根目录预置的 `sing-box.zip`
- 根目录重复的 `singbox-engine.json`
- 根目录重复的 `singbox-r-template.json`
- 空的 `app/src/main/jniLibs/arm64-v8a/.gitkeep`
- 本地残留的 `combined_refactor/ca-certificates.crt`
- 不再需要的旧独立二进制/预编译核心依赖路径

根目录旧版 `cfdata.go` / `go.mod` 暂时保留，因为它们属于历史源码；没有为了“清理”而删除整套旧入口，以降低误伤风险。

## CI action 基线

当前 candidate workflow 使用：

- `actions/checkout@v7`
- `actions/setup-go@v7`
- `actions/setup-java@v5`
- `android-actions/setup-android@v4`
- `gradle/actions/setup-gradle` v6.2.0（固定 commit）
- `actions/download-artifact@v8`
- `actions/upload-artifact@v7`
- Android NDK r28
- Go 1.26.8
- JDK 17

## Candidate 验证范围与限制

已完成：

- workflow YAML 解析
- JSON 配置解析
- Go 文件 `gofmt`
- stale action / stale schema / 旧二进制路径引用检查
- override 与当前 reF1nd testing 源代码的 API / 文件路径静态核对
- Android 构建流程与 AAR 依赖方向核对

当前运行环境只有 Go 1.23.2，且无外网工具链下载能力，因此无法在本地真正运行 Go 1.26.8 的 Libbox 编译或完整 Android Gradle 构建。最终的编译验证仍应由 GitHub Actions 完成；此次 CI 已将 Go 版本、source SHA、NDK、AAR 生成和安装流程统一起来。

## Candidate 版本编号

以上是 candidate1 当时的归档记录。candidate2 的当前归档见 `CFDATA-LIBBOX-V10-CANDIDATE.md`，只有完整构建成功时才使用 `v10` 作为成功归档编号。


## 2026-09-19 candidate2 追加审计

### Libbox 生产文件命名

`daemon/cfdata_true_test.go` 会被 Go 的正常生产构建排除。candidate2 改为 `daemon/cfdata_true.go`，并在 CI 中显式禁止旧 `_test.go` 文件回归。

### Provider 更新保留策略

reF1nd `ProviderRemote.StartContext` 会优先从现有 `path` 加载缓存，存在缓存时并不会在启动瞬间必然重新下载；因此“保留旧文件 + 原路径启动”不能同时满足“立即更新”。candidate2 使用同目录 `.next` 暂存路径触发初始抓取，验证 JSON 节点后再替换正式 `Provider1.json`。失败不触碰旧文件。

SFA 的 `UpdateProfileWork` / 手动更新逻辑也采用“下载新内容、校验、成功后才写入；异常时保持旧文件”的原则，本实现保持这一语义。

### 其他同步修正

发现并修复 `syncAllSingBoxSubscriptions` 中重复 `defer singBoxSyncMu.Unlock()`；这不是编译错误，但属于必须在进入实机测试前排除的运行时错误。
