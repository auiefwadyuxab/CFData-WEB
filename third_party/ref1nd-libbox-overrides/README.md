# reF1nd Libbox overrides

这两个 override 是 CFData-WEB 真连接测速的最小核心扩展。

- `daemon/cfdata_true.go`：通过当前运行中的 `StartedService` 和指定 outbound 执行真实 HTTP 延迟 / 下载测速。
- `experimental/libbox/cfdata.go`：仅负责把上述入口暴露给 gomobile / Android `CommandServer`。

CI 不提交 `libbox.aar`、standalone sing-box ELF 或其他预编译核心。每次 Android 构建都从 `reF1nd/sing-box-releases` 当前 testing build metadata 指向的 source SHA 重新编译，因此 override 始终与明确的 reF1nd testing 源码版本一起参与编译。
