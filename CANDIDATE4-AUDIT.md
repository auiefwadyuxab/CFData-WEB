# CFData-WEB v10 candidate4 审计记录

日期：2026-09-19

## Actions 35370575412 实际失败点

所有桌面 Go 构建均成功。

`Build reF1nd Libbox AAR (Android arm64)` 成功，说明 reF1nd testing source、CFData Libbox override、Go 1.26.8、NDK r28 这一条核心构建链已经实际通过 CI。

`Android APK` 的 SDK 安装也成功。

最终失败发生在 `Build release APK`：

- AGP：9.3.1
- Gradle：9.7.0
- `app/build.gradle` 仍应用 `org.jetbrains.kotlin.android`
- AGP 9 内置 Kotlin 已启用，因此主动拒绝旧 Kotlin Android plugin
- 同时项目仍使用旧 `android.kotlinOptions` DSL

错误原文核心：

`The 'org.jetbrains.kotlin.android' plugin is no longer required for Kotlin support since AGP 9.0.`

## candidate4 修复

1. 根目录 `build.gradle` 删除 `org.jetbrains.kotlin.android ... apply false`。
2. `app/build.gradle` 删除 `id "org.jetbrains.kotlin.android"`。
3. `app/build.gradle` 删除旧 `android.kotlinOptions`。
4. 不升级 AGP/Gradle：AGP 9.3 最低需要 Gradle 9.5，项目当前 Gradle 9.7 已满足。
5. CI 增加旧 Kotlin plugin/options 的早期检查。
6. CI 在 APK 构建前输出并验证 Gradle/AGP 工具链。

## sing-box 核心边界

保持 reF1nd Libbox 进程内真实出站；不引入 TUN、mixed inbound、standalone sing-box ELF 或 root 依赖。

真实延迟仍由 CFData 自己定义：3 次独立真实 HTTP、成功数、丢包率、平均/最小/最大延迟，并使用指定 outbound。

下载测速继续在延迟排序之后逐节点进行，避免并发下载互相抢占 100 Mbps 家宽带宽。

## 订阅缓存

Provider 刷新使用 `.next` 暂存路径。

只有暂存 Provider 可解析且节点数大于 0 时才提升到正式 Provider1.json；抓取/解析失败时保留上一份有效缓存。

## UI

新增 sing-box R 面板摘要卡片、响应式布局、粘性表头、更清楚的状态指示和真测速阶段显示。

延迟并发可选择 8/16/24/32/48/64，默认 16，并使用 localStorage 保存。

CSS 避免仅依赖 `color-mix()`，减少 Android WebView 兼容风险。

## 本地验证

- Node.js `--check`：singbox-ui.js 通过
- Python YAML：workflow 通过
- JSON：singbox-engine.json / singbox-r-template.json 通过
- Workflow 内全部 shell run block：`bash -n` 通过
- AGP 9 旧 Kotlin plugin/options guard：通过
- ZIP 内容完整性：完成后再次检查

## 限制

当前本地运行环境无法访问外部 Gradle/Go 下载源，因此不能在此环境伪造“完整 Android APK 已本地编译成功”。本次 candidate4 的 Android 核心 AAR 上游链路已经由 Actions 35370575412 实际成功验证；candidate4 新增的 Gradle 迁移需要下一次 Actions 进行最终编译验证。

版本状态：candidate，仅当完整 Actions 全部成功后才归档为正式 v10。
