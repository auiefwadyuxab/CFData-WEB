# CFData-WEB Libbox v8：移植说明

## 核心结论

这一版不再通过 `su`、独立 sing-box ELF、mixed 代理端口或 TUN 来做单节点真连接测试。Android 直接在 App 进程中初始化 reF1nd `Libbox`，长期持有一个 `CommandServer`/`StartedService`。

节点测试路径是：指定 outbound → sing-box `ResolveDialer` → CFData trace / 下载请求。

## 哪些 SFA 文件可以原样搬

严格按当前 CFData 工程来说，没有必要把 SFA 的 `BoxService.kt`、`VPNService.kt`、`ProxyService.kt` 等整套文件原样搬过来，因为 CFData 不需要 SFA 的服务状态、VPN TUN、root bridge、命令面板等 UI/系统能力。

可以直接复用/复制到工程中的构建文件：

- `gradlew`
- `gradlew.bat`
- `gradle/wrapper/gradle-wrapper.jar`
- `gradle/wrapper/gradle-wrapper.properties`

这几个已经放进本次 ZIP。

SFA 的运行代码已经按 CFData 的包名和无 root 目标做了裁剪，因此下面这些不是“无改动复制”：

- `Application.kt` → `app/src/main/java/com/cfdata/web/singbox/CFDataApplication.kt`
- `DefaultNetworkMonitor.kt` → `app/src/main/java/com/cfdata/web/singbox/DefaultNetworkMonitor.kt`
- `PlatformInterfaceWrapper.kt` → `app/src/main/java/com/cfdata/web/singbox/CFDataPlatformInterface.kt`
- SFA 的 service/UI 层没有整体搬入。

## 两个必须进入 reF1nd sing-box 源码树的完整文件

如果手工构建 reF1nd `Libbox`，把以下两个文件按目录放进去：

`third_party/ref1nd-libbox-overrides/daemon/cfdata_true_test.go`
→ `reF1nd/sing-box/daemon/cfdata_true_test.go`

`third_party/ref1nd-libbox-overrides/experimental/libbox/cfdata.go`
→ `reF1nd/sing-box/experimental/libbox/cfdata.go`

本项目的 GitHub Actions 已经自动完成这两次复制，不需要手工操作。

## 真连接测试的实际调用

`CFDataTrueTest` 不创建本地代理入口，不启动新的进程，不检查 root。它直接从当前 `StartedService` 找指定 outbound，然后创建 HTTP Transport：其 `DialContext` 指向 sing-box `ResolveDialer`。因此 HTTP/TLS/目标站点的连接实际经过该 sing-box outbound。

## Provider

Provider1 仍由 reF1nd `ProviderRemote` 负责：URL 拉取、Base64 解码、订阅解析、exclude/include、outbound 创建，以及 `Provider1.json` 持久化。

Provider1 缓存如果带有 `# upload=...` 等 subscription-userinfo 首行，CFData v8 也会正确剥离该首行再解析 JSON。

## 构建限制

本沙盒没有可用的 Go 1.25.4 本地工具链，也不能下载 Gradle 9.7.0 / reF1nd AAR 所需的远程依赖，因此这里完成的是源码级、格式级和工作流级审计；实际 APK 编译必须由 GitHub Actions 使用 Go 1.25.4 + reF1nd `reF1nd-testing` 生成 `libbox.aar` 后再验证。
