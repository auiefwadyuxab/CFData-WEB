# CFData-WEB v10 candidate5 审计记录

日期：2026-09-19

## Actions 35370575412 实际结果

该运行中：

- 6 个桌面 Go 构建成功。
- reF1nd testing source 解析成功。
- Build reF1nd Libbox AAR 成功。
- Android SDK 安装成功。
- Gradle 9.7.0 启动成功。
- Android backend 编译成功。
- 最终没有进入 APK 编译。

## candidate4 的实际失败根因

失败发生在 `Verify Gradle / AGP toolchain` 自检步骤，而不是 Android/Kotlin 编译。该检查使用 `grep -Fq 'version \"9.3.1\"' build.gradle`，在单引号 shell 字符串里反斜杠会被当作字面字符，无法匹配真实的 `version "9.3.1"`，因此错误退出。

## candidate5 修复

1. AGP 9.3.1 检查改成 Python 正则结构匹配，不再依赖易错的转义固定字符串。
2. 增加 `./gradlew help --no-daemon --stacktrace --warning-mode all`，让 Gradle 真正执行一次项目配置，提前暴露构建脚本问题。
3. 保留 AGP 9 内置 Kotlin：根目录和 app 模块均不再应用 `org.jetbrains.kotlin.android`，也不再使用 `kotlinOptions`。这一做法符合 Android 官方 AGP 9 内置 Kotlin 迁移要求。

## UI candidate5

- 增加节点关键词搜索：名称、server、port、协议、来源。
- 增加结果筛选：全部、仅通过、仅失败、未测试。
- 增加真实测速进度条：延迟阶段使用不确定进度，下载阶段显示按节点完成度。
- 测试期间禁用会产生状态竞争的编辑、同步、刷新和测试参数控件。
- 小屏幕下筛选控件高度统一，更适合 Android WebView。
- 保留 CFData 原本的 3 次真实延迟、丢包率、平均/最小/最大延迟排序，以及排序后的逐节点下载测速。

## sing-box / Provider 边界

仍使用 reF1nd Libbox 进程内真实 outbound；不引入 TUN、mixed inbound、standalone sing-box ELF 或 root。Provider 刷新继续使用 `.next` 暂存，仅在新 Provider 可解析且有节点后替换正式缓存；失败保留上一份有效缓存。

## 验证状态

本地可执行的验证必须全部通过后才归档此 candidate：JS syntax、YAML、JSON、Workflow shell、AGP declaration、旧 Kotlin plugin/options 残留、ZIP integrity。

当前环境无法联网下载完整 Gradle/Go 依赖，因此最终 APK 成功与否仍以 GitHub Actions 为准。版本状态：candidate5，尚未占用正式 v10 编号。
