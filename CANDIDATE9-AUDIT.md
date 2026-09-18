# CFData-WEB Candidate 9 Audit — 2026-09-19

## 本次修复针对的实际运行问题

用户提供的 Android sing-box 运行崩溃日志反复出现同一个 panic：

`netip.ParsePrefix("fe80::...%rmnet_data2/64")`: IPv6 zones cannot be present in a prefix

调用链进入 `experimental/libbox.PlatformInterface.NetworkInterfaces()`，随后 reF1nd sing-box 使用 `netip.MustParsePrefix` 解析 Android 平台回传的 interface address。

根因：Android `Inet6Address.hostAddress` 可能包含 `%rmnet_data2` 一类的 IPv6 scope zone，而 sing-box prefix parser 不接受带 zone 的 prefix。

## Candidate 9 修复

### 1. Android PlatformInterface

`app/src/main/java/com/cfdata/web/singbox/CFDataPlatformInterface.kt`

- IPv6 地址改为使用原始 16-byte address 重建 `Inet6Address`，再生成 `host/prefix`，从而去掉 `%interface` zone。
- 按 SFA/reF1nd 侧的接口模型填写 `IFF_UP | IFF_RUNNING`，并补充 loopback / point-to-point / multicast flags。
- 保留现有 DNS、默认网关、MTU、interface type、metered 等信息。

### 2. Android Bridge 诊断

- `CFDataSingBoxCore.status()` 返回 Core / Service / test-config 状态。
- `MainActivity.singBoxStatus(String)` 与统一 `bridgeCall()` 参数模型匹配。
- 诊断 UI 会显示 Provider1 是否有效、节点数量、配置是否存在、Core 是否已经启动。

### 3. UI

- 订阅名称与订阅 URL 分行；URL 独占一整行，减少触控拥挤。
- 请求头独占一行。
- 真连接测试参数改为更紧凑的双列布局；测速 URL 控件收紧。
- 顶部说明与操作区之间的间距缩小；卡片、表格整体减小空白。
- 单节点真连接失败时，直接显示 Android/sing-box 返回的具体错误。
- 批量测试结束时显示前几个失败节点的具体错误。

### 4. Gradle Wrapper

ZIP 中包含 `gradle/wrapper/gradle-wrapper.jar`，并使用 Gradle 9.6.1 Wrapper；这是上一候选版缺失导致 CI 在真正编译前失败的文件。

## 静态检查

- `node --check app/src/main/assets/singbox-ui.js`：通过
- `gofmt -d`：无差异
- Wrapper JAR `unzip -t`：通过
- Wrapper JAR SHA-256：`497c8c2a7e5031f6aa847f88104aa80a93532ec32ee17bdb8d1d2f67a194a9c7`
- `build.gradle` / `app/build.gradle`：AGP 9.3.1 + built-in Kotlin + BuildConfig
- PlatformInterface notification ABI methods：存在
- Java-facing CFDataSingBoxCore methods：`@JvmStatic` 检查通过
- `cfdata_true_test.go`：不存在，生产实现仍位于 `cfdata_true.go`

## 关于本地完整 Gradle 编译

当前运行环境无法访问 `services.gradle.org`，因此本地 `./gradlew --version` 无法下载 Gradle 9.6.1 distribution，不能把“本地完整 APK 编译通过”冒充为已验证事实。

CI 环境仍应使用仓库内 Wrapper + `services.gradle.org` 正常获取 distribution 后进行完整编译。
