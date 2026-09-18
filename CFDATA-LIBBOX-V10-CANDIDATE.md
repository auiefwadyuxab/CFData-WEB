# CFData-WEB v10 candidate：Real Ping/真连接重新审计

## v2rayNG 对照结论

v2rayNG 当前 RealPingWorkerService 并不是依赖已经启动的 VPN/TUN 才能测。它为每个节点生成用于测速的精简 Xray 配置，然后调用 `Libv2ray.measureOutboundDelay`；Xray 侧会新建一个轻量 core instance、只保留 outbound/dispatcher/log，启动后通过 `core.Dial` 让 HTTP 请求直接走目标 outbound。

因此本项目的“sing-box 真连接”在概念上与它一致：不经过本地 HTTP/SOCKS 入站，也不要求启动 VPN/TUN；直接由 in-process sing-box core 的目标 outbound 建立真实请求。CFData 当前使用长生命周期 CommandServer/service，而不是每个节点重新创建 core，这是为了减少频繁启动/销毁核心的开销。

## 本候选版本的关键修正

- 延迟测试默认并发 16，最高 64；保持每个节点内部 3 次请求顺序与 variant fallback 顺序。
- 一个 batch 先建立一次 test config，再并行调用不同 outbound 的真实 HTTP 探测。
- 下载测速仍然串行，避免 100 Mbps 家宽在多个 6 秒下载之间相互争抢，保持结果可比较性。
- 不复制 v2rayNG 的简单 TCP 预检，因为它只是 fail-fast 优化，并非真实代理延迟本身；对 CF/IP/伪装等节点还可能造成误判。
- v2rayNG 当前实现成功条件是 2 次测量中至少 1 次成功并返回最小耗时；CFData 保留 3 次、丢包率、平均/最小/最大延迟，因此更适合作为节点比较数据源。

## 构建链

- reF1nd testing source 来自 `sing-box-releases/dev/testing-build-info.json` 的 source SHA。
- 当前 metadata：`1.15.0-alpha.6-reF1nd` / `9e5ea2101d9dbd4194f877814bb49b3de8c1487b`。
- Libbox AAR 在 CI 临时构建，不提交到 CFData 源码。
- 当前 reF1nd SFA testing 源码使用 `compileSdk = 37`、`compileSdkMinor = 1`、NDK `28.0.13004108`；CFData Android 模块同步到 minor 1。

## 本候选版命名

这是从 v9 修正出来的候选版本，不能提前称为已成功的 v10。压缩包采用 `v10-candidate1` 命名；只有 GitHub Actions 全链路通过后，下一个成功归档才使用 v10。
