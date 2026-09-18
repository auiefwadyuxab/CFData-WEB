# CFData-WEB reF1nd Libbox v9：真连接测速架构

## 定位

这个工程现在不再把“Fork 兼容”当作架构边界。它的定位是：

**SFA / reF1nd Libbox 负责网络连接能力；CFData 负责节点整理、测试策略、排序、测速窗口和 WebUI。**

也就是说，CFData 的价值保留在“怎么比较节点”，而真正的代理连接、协议解析、TLS、DNS、detour、multiplex、各种 outbound 能力交给 reF1nd sing-box 核心。

## 本次重新审计的架构结论

这次重新核对后，决定**不把 `sing-box-for-android` 整个项目拷进 CFData，也不把 release APK/AAR 当作运行时依赖**。SFA 源码主要用于参考正确的 `CommandServer` / `StartedService` 生命周期；真正被 CFData 集成的是 `reF1nd/sing-box` 生成的 Libbox AAR。

原因很直接：CFData 不是一个完整 VPN 客户端，不需要 TUN、系统代理、root shell、Shizuku、通知、Compose UI 等 SFA 应用层功能。把这些一起带入会显著增加构建和维护面，而且会让“真连接测试”与“系统 VPN 运行”纠缠在一起。

当前实现因此保持三层边界：

`CFData Web/Android 壳 → CommandServer → reF1nd Libbox outbound`

其中只有 Libbox AAR 来自 reF1nd；Android 生命周期和批量测试调度由 CFData 自己掌控。

## Android 核心

Android 端在 App 进程内初始化 reF1nd Libbox，并长期持有一个 `CommandServer` / `StartedService`，整体形态参考 SFA 的 core/service 组织方式。

真连接测试不：

- 不启动 standalone sing-box ELF
- 不使用 `su`
- 不要求 root
- 不开 mixed HTTP/SOCKS 入站再绕一圈
- 不用 TUN 去模拟节点测试
- 不用 TCPing / HTTPing 的倍率修正

单节点测试路径是：

`指定 outbound → sing-box ResolveDialer → Go net/http → CF trace / 下载 URL`

这意味着测试流量本身就是由目标 outbound 建立，而不是“先连本机代理，再由本机代理转发”的间接测法。

## 真连接延迟

默认：3 次独立 HTTP 请求。

目标：

`https://speed.cloudflare.com/cdn-cgi/trace`

每一次请求都：

1. 新建 HTTP 请求
2. 通过指定 sing-box outbound
3. 记录 outbound Dial 时间
4. 记录 TLS handshake 时间（HTTPS 时）
5. 记录 TTFB
6. 读取 trace 返回的 `ip` / `colo`
7. 完整关闭本次连接

结果包含：

- 成功次数 / 总次数
- 丢包率
- 平均真实 TTFB
- 最小真实 TTFB
- 最大真实 TTFB
- outbound 建连耗时
- TLS 握手耗时
- 出站 IP
- Cloudflare Colo
- 每一次请求的错误

这里不再存在原 CFData HTTPing 的 ×1.3 / ×4.0 延迟倍率。对于这个项目来说，测到什么就显示什么。

### 为什么同时保存两个延迟

“outbound 建连耗时”更接近节点自身连接建立成本；“TTFB”则是从真实请求发出到目标站点第一次回包的端到端表现。

最终排序默认采用 **TTFB + 丢包率**，同时把 outbound 建连和 TLS 数据展示出来，避免把代理握手时间与远端服务器响应时间混成一个看不见内部差异的数字。

## 延迟排序

批量测试第一阶段完成后才排序。

排序规则：

1. 成功节点优先
2. 丢包率低优先
3. 平均真实 TTFB 低优先
4. 最大真实 TTFB 低优先
5. 名称作为最终稳定排序条件

这一层仍然是 CFData 的“比较节点”思路，只是输入数据换成了真正的代理连接结果。

## 真连接下载测速

第二阶段严格在第一阶段排序之后进行。

对通过延迟测试的节点，使用其第一阶段实际成功的 `testedOutboundTag`，而不是重新猜测其它 variant。

测速目标 URL 保留原 CFData 的选择：

- 自动选择
- Cloudflare：`speed.cloudflare.com/__down?bytes=99999999`
- CM 提供：`cf.090227.xyz/__down?bytes=99999999`
- 移动专属：`speed.okl.abrdns.com`
- 自定义 URL

默认测速窗口：**6 秒**。

测速方式沿用 CFData 原来的窗口思路：HTTP 头部取得以后开始计时，在固定窗口内持续读取响应体，最终：

`真实收到的字节数 / 实际测速窗口 = MB/s`

它不再用“下载到固定 10 MiB 就结束”作为速度测量逻辑；`download_test_bytes` 仅作为旧配置兼容字段保留。

### 批量测速顺序

`全部节点真实延迟 3 次 → 排序 → 依排序逐节点 6 秒测速`

因此速度测试不会因为前面某个节点慢就提前改变候选顺序，也不会出现“速度先测了，再按速度倒推排序”的混乱。

节点之间默认保留约 1.2 秒间隔，继续降低家庭网络瞬时拥塞对后续节点的影响。

## Variant / 多订阅去重

同一 `server:port` 的节点仍然合并，来源全部保留。

一个节点可以有多个 provider variant：

- 第一阶段：主 outbound 失败时依次尝试 variant
- 一旦某个 outbound 的 3 次真实延迟测试成功，就记录 `testedOutboundTag`
- 第二阶段速度测试只使用这个实际成功的 outbound

这样速度数据不会错误地跑到另一个 variant 上。

ProviderRemote 保存缓存时的原始 tag 与运行时的 `ProviderTag/tag` 命名空间也会在 CFData 后端重建时做归一化，避免多个订阅之间发生 detour/tag 冲突。

## Provider

订阅拉取继续使用 reF1nd 的 `ProviderRemote`：

- URL 拉取
- Base64 / 订阅解析
- exclude / include
- outbound / endpoint 创建
- Provider1 缓存
- subscription-userinfo

订阅 HTTP 请求头支持通过 reF1nd 的 `http_client.headers` 传入，例如 v2rayNG 风格的 `User-Agent`、`Connection`、`Accept-Encoding`。

Provider 的 `download_detour` 不再写入新配置，因为 reF1nd `reF1nd-testing` 的 Provider schema 已提供 `http_client`，并且 ProviderRemote 在 `http_client` 与旧 `download_detour` 同时存在时会直接视为冲突。

如果 Provider 缓存有 `# upload=...; download=...` 等首行，CFData 解析前会先去掉首行。

## SFA 能力与 CFData 的关系

Libbox AAR 本身保留 reF1nd sing-box 的核心能力。CFData 没有为了“看起来简单”去删协议或核心能力。

当前 CFData 自己关闭的仅是与本项目目标冲突的系统控制入口，例如 root shell、root bridge、auto-redirect/TUN 控制等；这是因为本项目现在只是一个“前端驱动的节点真实性测试器”，而不是完整替代 SFA 的 VPN 客户端。

## 构建

GitHub Actions：

1. 使用 Go 1.26.8 构建 CFData Web 后端；这是当前 reF1nd testing 发布链正在使用的 Go 工具链
2. 从 `reF1nd/sing-box-releases` 的 `testing-build-info.json` 解析当前 testing 构建对应的 source SHA，并精确 checkout `reF1nd/sing-box`
3. 注入本项目两个完整 override 文件后直接从该 source SHA 编译
4. 按 reF1nd 官方构建方式执行 `make lib_install` 与 `cmd/internal/build_libbox`，生成 arm64 `libbox.aar`
5. AAR 仅作为 CI artifact 传递，构建前临时复制到 `app/libs/libbox.aar`；仓库不保存二进制
6. 使用当前 `reF1nd/sing-box-for-android` 的 `reF1nd-testing` 系列构建环境和当前 Android 17 / API 37 SDK 构建 APK

Android 17 / API 37 目前属于 Cinnamon Bun Preview；Actions 使用与当前 SDK 发布方式匹配的 `platforms;android-37.0` 和 Build-Tools 37。

## v9 与 v8 的关键变化

v8 的真连接入口已经可以直接走 sing-box outbound，但测速仍偏向“组合式真测试”。

v9 明确把 CFData 原始工作流拆成两个阶段：

**阶段 A：真实 HTTP 延迟统计**

`3 次 / 丢包 / TTFB / outbound 建连 / TLS / IP / Colo`

**阶段 B：真实连续下载速度**

`按阶段 A 排序 / 使用实际成功 outbound / 固定 6 秒窗口 / MB/s`

这样核心职责边界就很清楚：

`SFA/reF1nd = 怎么连`

`CFData = 怎么测、怎么比、怎么显示`

## CI 构建说明

2026-09-18 重新审计：reF1nd 当前 testing build metadata 指向 `1.15.0-alpha.6-reF1nd`，source SHA 为 `9e5ea2101d9dbd4194f877814bb49b3de8c1487b`。本项目 CI 不再自己猜版本、也不把旧 sing-box 压缩包或 AAR 固化进仓库；每次构建均按当前 testing 发布元数据取对应源码。

当前 Android 17 SDK 的平台包采用 minor-version 坐标。工程使用 `compileSdk 37` + `compileSdkMinor 0`，Actions 安装 `platforms;android-37.0` 与 `build-tools;37.0.0`；不会再请求不存在的 `platforms;android-37`。

## 真连接测量原则

- 不把 TCPing 当作代理节点延迟，因为它只测本机直连目标 IP:端口。
- 不采用 HTTPing 的 TLS 倍率；倍率只是原项目为了分级展示的经验系数。
- 真延迟使用指定 sing-box outbound 发起独立的 HTTPS `GET /cdn-cgi/trace` 请求，默认 3 次，统计真实 TTFB，并同时记录 outbound 建连与 TLS 握手耗时。
- 延迟阶段结束后才排序；排序依据保持 CFData 的比较思路：成功优先、丢包更低、平均延迟更低、最大延迟更低。TLS 证书校验与时间来源复用 Libbox/SFA 的运行时上下文。
- 下载阶段只对延迟成功节点进行，严格使用延迟阶段确认可工作的同一个 outbound；请求头后开始计时，连续读取 6 秒，速度 = 实际读取字节 / 实际窗口时间。
