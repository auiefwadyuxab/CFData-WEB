# CFData-WEB v10 candidate6 audit — 2026-09-19

## 本轮 CI 实际失败

GitHub Actions run 35376033025 / Android job 105701421821 已通过：
- reF1nd testing source resolve
- 6 个桌面 Go 构建
- reF1nd Libbox AAR source build
- Android SDK installation
- CFData Android backend build
- Gradle toolchain configuration
- Android resource/manifest processing

最终失败发生在 `:app:compileReleaseKotlin`。

实际编译错误：
1. `CFDataApplication.kt` 引用 `com.cfdata.web.BuildConfig`，但 AGP 9 默认不生成 BuildConfig。
2. `CFDataPlatformInterface.kt` 缺少当前 reF1nd Libbox PlatformInterface 的 `sendNotification` / `cancelNotification` 两个 ABI 方法。
3. `compileSdkMinor 1` 使用了 Gradle Groovy 的旧 space-assignment 写法，Gradle 9.7 给出将于 Gradle 10 移除的弃用警告。

## 本轮修复

- `app/build.gradle`
  - `buildFeatures { buildConfig = true }`
  - `compileSdkMinor = 1`
  - 相关 Gradle DSL 改为显式 `=` 赋值。
- `CFDataPlatformInterface.kt`
  - 增加 `sendNotification(notification: Notification)` 空实现。
  - 增加 `cancelNotification(identifier: String, typeID: Int)` 空实现。
  - CFData 不使用系统通知/VPN 通知能力，因此保持 no-op。
- `CFDataSingBoxCore.kt`
  - Provider 刷新仍然先下载到 `.next`。
  - `.next` 验证成功后提升为正式 `Provider1.json`。
  - **立即用正式 Provider 路径重新加载长期运行的 Libbox service**，避免 active service 长期绑定 `.next`。
- `.github/workflows/build.yml`
  - 增加 BuildConfig / PlatformInterface ABI / explicit DSL 检查。
  - 增加 `./gradlew help --no-daemon --stacktrace --warning-mode all`。
  - 增加独立 `:app:compileReleaseKotlin --no-daemon --stacktrace`，让下一次 CI 可以单独验证 Kotlin 编译阶段。

## 当前工具链依据

截至 2026-09-19，Android 官方已发布 AGP 9.4.0。AGP 9.x 默认使用 Built-in Kotlin；不应继续应用 `org.jetbrains.kotlin.android`。AGP 9.3 支持 API 37，Gradle 9.5+；本项目当前使用 AGP 9.3.1 + Gradle 9.7.0，满足该兼容范围，因此本轮不额外升级 AGP，降低无关变量。

## 本地可验证项

通过：
- `CFDataPlatformInterface.kt` against a reconstructed reF1nd PlatformInterface ABI stub: Kotlin compiler succeeded.
- JS syntax: `node --check app/src/main/assets/singbox-ui.js`。
- UI `getElementById` 静态引用完整，无缺失 ID。
- JSON 静态解析。
- Workflow shell blocks syntax check。
- AGP built-in Kotlin / BuildConfig / compileSdkMinor static checks。
- ZIP integrity。

无法在本地执行：
- `go test ./...`：沙箱只有 Go 1.23.x，仓库要求 Go 1.26.8，且沙箱不能联网下载 toolchain。
- 完整 Android Gradle APK：沙箱没有 GitHub Actions 同等 Android SDK/Gradle/AAR 构建环境。

## UI 状态

candidate5 已将 sing-box R 面板整理为手机友好布局，包含：
- 订阅/节点/通过/最快摘要卡片
- Provider 编辑与请求头
- 可保存的 8/16/24/32/48/64 延迟并发
- 节点搜索与结果筛选
- 真延迟/下载阶段状态与进度
- 固定表头、横向滚动、移动端响应式布局

本轮没有为了“好看”再引入新的复杂依赖，避免 UI 变化成为新的构建变量。


# Candidate7 增补审计（2026-09-19）

针对 GitHub Actions run `35378146752` / job `105708213153`。

本次 CI 已成功完成：reF1nd Libbox AAR、Android SDK、Android backend、AGP/Kotlin 配置以及 Kotlin 编译。最终在 `:app:compileReleaseJavaWithJavac` 失败。

精确错误为 MainActivity.java 通过 `CFDataSingBoxCore.syncProviders(...)`、`trueLatencyTest(...)`、`trueSpeedTest(...)`、`trueTest(...)`、`stop()` 调用 Kotlin `object` 中的实例方法。

修复：五个 Java-facing API 增加 `@JvmStatic`。Kotlin 官方 Java 互操作文档说明，named object 中的函数使用 `@JvmStatic` 后才会生成可通过对象类名直接调用的静态方法。

同时增加 `Preflight Kotlin / Java bridge`，在完整 APK 构建前先明确执行 Kotlin 与 Java 编译。
