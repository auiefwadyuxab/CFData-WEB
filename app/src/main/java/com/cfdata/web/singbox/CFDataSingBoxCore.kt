package com.cfdata.web.singbox

import io.nekohasekai.libbox.CommandServer
import io.nekohasekai.libbox.CommandServerHandler
import io.nekohasekai.libbox.OverrideOptions
import io.nekohasekai.libbox.PlatformInterface
import io.nekohasekai.libbox.SystemProxyStatus
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.Callable
import java.util.concurrent.ExecutorCompletionService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * SFA-style long-lived Libbox core used by CFData's WebView UI.
 *
 * The core owns real sing-box outbounds. CFData only decides which outbound to
 * test, how many times to repeat latency probes, how to sort them, and how long
 * to run the download-speed window.
 */
object CFDataSingBoxCore {
    private const val DEFAULT_REPEAT = 3
    private const val DEFAULT_LATENCY_TIMEOUT_SECONDS = 3
    private const val DEFAULT_LATENCY_CONCURRENCY = 16
    private const val MAX_LATENCY_CONCURRENCY = 64
    private const val DEFAULT_SPEED_DURATION_SECONDS = 6
    private const val DEFAULT_DOWNLOAD_URL = "https://speed.cloudflare.com/__down?bytes=99999999"
    private const val TRACE_URL = "https://speed.cloudflare.com/cdn-cgi/trace"
    private const val CORE_MODE = "android-sfa-style-libbox-service"

    private val lifecycleLock = Any()
    private val operationLock = Any()
    private var platform: PlatformInterface? = null
    private var commandServer: CommandServer? = null
    private var started = false
    private var testConfigDigest: String? = null

    private val handler = object : CommandServerHandler {
        override fun serviceStop() {
            stop()
        }

        override fun serviceReload() {
            throw IllegalStateException("CFData directly controls the test profile")
        }

        override fun getSystemProxyStatus(): SystemProxyStatus? = SystemProxyStatus().apply {
            available = false
            enabled = false
        }

        override fun setSystemProxyEnabled(isEnabled: Boolean) {
            throw IllegalStateException("system proxy is not managed by CFData")
        }

        override fun triggerNativeCrash() {
            throw IllegalStateException("debug crash is disabled")
        }

        override fun writeDebugMessage(message: String?) {
            if (!message.isNullOrBlank()) {
                android.util.Log.d("CFDataSingBox", message)
            }
        }

        override fun connectSSHAgent(): Int = -1
    }

    fun reload(configContent: String) {
        require(configContent.isNotBlank()) { "sing-box config is empty" }
        synchronized(lifecycleLock) {
            ensureServerLocked()
            commandServer!!.startOrReloadService(
                configContent,
                noSystemRoutingOptions(),
            )
            started = true
            testConfigDigest = null
        }
    }

    @JvmStatic
    fun syncProviders(payload: String): String = synchronized(operationLock) {
        val request = JSONObject(payload)
        val configs = request.optJSONArray("configs") ?: JSONArray()
        val syncTimeoutSeconds = request.optInt("timeoutSeconds", 90).coerceIn(1, 300)
        val results = JSONArray()
        val syncErrors = mutableListOf<String>()

        for (index in 0 until configs.length()) {
            val item = configs.optJSONObject(index) ?: continue
            val name = item.optString("name").trim()
            val id = item.optString("id").trim()
            val providerPath = item.optString("providerPath").trim()
            var configText = item.optString("config")
            val result = JSONObject().apply {
                put("id", id)
                put("name", name)
                put("providerPath", providerPath)
                put("configPath", item.optString("configPath"))
                put("success", false)
                put("nodeCount", 0)
            }

            try {
                if (configText.isBlank()) {
                    throw IllegalArgumentException("订阅 $name 的 Core 配置为空")
                }
                if (providerPath.isBlank()) {
                    throw IllegalArgumentException("订阅 $name 的 Provider 路径为空")
                }

                // Never delete the last known-good Provider1.json before a refresh.
                // reF1nd ProviderRemote treats an existing path as cache and may
                // therefore skip the immediate fetch. Stage the refresh into a
                // sibling *.next path: a missing staged file forces the provider to
                // perform its initial fetch, and the old permanent file remains
                // untouched until the new provider has been parsed and validated.
                val stagedPath = providerPath + ".next"
                deleteStagedProvider(stagedPath)
                val stagedConfig = stageProviderConfig(configText)
                runSyncAttempt(stagedConfig)
                val stagedCount = waitForProviderNodes(stagedPath, syncTimeoutSeconds)
                if (stagedCount <= 0) {
                    deleteStagedProvider(stagedPath)
                    throw IllegalStateException("Provider1.json 未生成可识别节点；已保留上一份有效缓存")
                }

                // Promote only after the staged provider is confirmed valid. The
                // helper keeps a temporary backup so a failed rename restores the
                // previous Provider1.json rather than losing the last good cache.
                promoteStagedProvider(stagedPath, providerPath)

                // The staged config above is only a transactional download target.
                // Rebind the long-lived Libbox service to the canonical Provider1.json
                // path after promotion. This prevents the active service from keeping
                // the *.next path forever (which would make the next sync mutate/remove
                // the currently active cache file).
                runSyncAttempt(configText)
                val finalCount = waitForProviderNodes(providerPath, syncTimeoutSeconds)
                if (finalCount <= 0) {
                    throw IllegalStateException("Provider1.json 已替换，但重新加载正式 Provider 失败")
                }

                result.put("success", true)
                result.put("nodeCount", finalCount)
            } catch (e: Exception) {
                val message = e.message ?: e.toString()
                result.put("error", message)
                syncErrors += "$name: $message"
            }
            results.put(result)
        }

        JSONObject().apply {
            put("success", syncErrors.isEmpty())
            put("results", results)
            put("errors", JSONArray(syncErrors))
            put("mode", CORE_MODE)
        }.toString()
    }

    /** Run only the true HTTP latency stage for all supplied nodes. */
    @JvmStatic
    fun trueLatencyTest(payload: String): String = synchronized(operationLock) {
        val request = JSONObject(payload)
        val nodes = request.optJSONArray("nodes") ?: JSONArray()
        if (nodes.length() == 0) throw IllegalArgumentException("没有可测试节点")

        val repeat = request.optInt("repeat", DEFAULT_REPEAT).coerceIn(1, 10)
        val timeoutSeconds = request.optInt("timeout", DEFAULT_LATENCY_TIMEOUT_SECONDS).coerceIn(1, 60)
        val concurrency = request.optInt("concurrency", DEFAULT_LATENCY_CONCURRENCY).coerceIn(1, MAX_LATENCY_CONCURRENCY)
        ensureTestConfig(nodes)
        val server = commandServer ?: throw IllegalStateException("Libbox Core 未启动")

        val orderedResults = runLatencyBatch(server, nodes, repeat, timeoutSeconds, concurrency)
        val results = JSONArray()
        var passed = 0
        for (result in orderedResults) {
            if (result == null) continue
            if (result.optBoolean("success")) passed++
            results.put(result)
        }
        return@trueLatencyTest JSONObject().apply {
            put("success", true)
            put("total", results.length())
            put("passed", passed)
            put("results", results)
            put("mode", CORE_MODE)
            put("repeat", repeat)
            put("timeout", timeoutSeconds)
            put("concurrency", concurrency)
        }.toString()
    }

    /** Run only the true download stage, in the caller-provided latency order. */
    @JvmStatic
    fun trueSpeedTest(payload: String): String = synchronized(operationLock) {
        val request = JSONObject(payload)
        val nodes = request.optJSONArray("nodes") ?: JSONArray()
        if (nodes.length() == 0) throw IllegalArgumentException("没有可测速节点")

        val durationSeconds = request.optInt("duration", DEFAULT_SPEED_DURATION_SECONDS).coerceIn(1, 120)
        val downloadUrl = request.optString("downloadUrl", DEFAULT_DOWNLOAD_URL).trim().ifBlank { DEFAULT_DOWNLOAD_URL }
        ensureTestConfig(nodes)
        val server = commandServer ?: throw IllegalStateException("Libbox Core 未启动")

        val results = JSONArray()
        var passed = 0
        for (index in 0 until nodes.length()) {
            val node = nodes.optJSONObject(index) ?: continue
            val result = testSpeedExactOutbound(server, node, durationSeconds, downloadUrl)
            if (result.optBoolean("success")) passed++
            results.put(result)
        }
        return@trueSpeedTest JSONObject().apply {
            put("success", true)
            put("total", results.length())
            put("passed", passed)
            put("results", results)
            put("mode", CORE_MODE)
            put("duration", durationSeconds)
            put("downloadUrl", downloadUrl)
        }.toString()
    }

    /** Compatibility entry point for older WebView builds: latency, then speed. */
    @JvmStatic
    fun trueTest(payload: String): String = synchronized(operationLock) {
        val request = JSONObject(payload)
        val nodes = request.optJSONArray("nodes") ?: JSONArray()
        if (nodes.length() == 0) throw IllegalArgumentException("没有可测试节点")

        val repeat = request.optInt("repeat", DEFAULT_REPEAT).coerceIn(1, 10)
        val timeoutSeconds = request.optInt("timeout", DEFAULT_LATENCY_TIMEOUT_SECONDS).coerceIn(1, 60)
        val concurrency = request.optInt("concurrency", DEFAULT_LATENCY_CONCURRENCY).coerceIn(1, MAX_LATENCY_CONCURRENCY)
        val durationSeconds = request.optInt("duration", DEFAULT_SPEED_DURATION_SECONDS).coerceIn(1, 120)
        val downloadUrl = request.optString("downloadUrl", DEFAULT_DOWNLOAD_URL).trim().ifBlank { DEFAULT_DOWNLOAD_URL }
        ensureTestConfig(nodes)
        val server = commandServer ?: throw IllegalStateException("Libbox Core 未启动")

        val latencyResults = runLatencyBatch(server, nodes, repeat, timeoutSeconds, concurrency)
        val results = JSONArray()
        var passed = 0
        for (index in 0 until nodes.length()) {
            val node = nodes.optJSONObject(index) ?: continue
            val latency = latencyResults.getOrNull(index) ?: JSONObject().apply {
                put("success", false)
                put("successCount", 0)
                put("totalAttempts", repeat)
                put("lossRate", 100.0)
                put("mode", CORE_MODE)
                put("error", "延迟测试没有返回结果")
            }
            if (!latency.optBoolean("success")) {
                results.put(latency)
                continue
            }
            val speed = testSpeedExactOutbound(server, JSONObject(node.toString()).apply {
                put("testedOutboundTag", latency.optString("testedOutboundTag"))
            }, durationSeconds, downloadUrl)
            val combined = JSONObject(latency.toString())
            copyAll(combined, speed)
            if (combined.optBoolean("success")) passed++
            results.put(combined)
        }

        return@trueTest JSONObject().apply {
            put("success", true)
            put("total", results.length())
            put("passed", passed)
            put("results", results)
            put("mode", CORE_MODE)
            put("latencyConcurrency", concurrency)
        }.toString()
    }

    /**
     * Run node latency probes in parallel with a bounded worker pool, while
     * preserving input order in the returned list. The sing-box service is
     * created/reloaded once before this function is entered, so workers only
     * perform direct outbound HTTP probes and never mutate core lifecycle state.
     */
    private fun runLatencyBatch(
        server: CommandServer,
        nodes: JSONArray,
        repeat: Int,
        timeoutSeconds: Int,
        concurrency: Int,
    ): List<JSONObject?> {
        val executor = Executors.newFixedThreadPool(concurrency)
        val completion = ExecutorCompletionService<Pair<Int, JSONObject>>(executor)
        var submitted = 0
        try {
            for (index in 0 until nodes.length()) {
                val node = nodes.optJSONObject(index) ?: continue
                completion.submit(Callable {
                    val result = try {
                        testLatencyWithVariants(server, node, repeat, timeoutSeconds)
                    } catch (e: Exception) {
                        baseNodeResult(node).apply {
                            put("success", false)
                            put("successCount", 0)
                            put("totalAttempts", repeat)
                            put("lossRate", 100.0)
                            put("mode", CORE_MODE)
                            put("variantAttempts", 0)
                            put("error", e.message ?: e.toString())
                        }
                    }
                    index to result
                })
                submitted++
            }

            val ordered = arrayOfNulls<JSONObject>(nodes.length())
            repeat(submitted) {
                val (index, result) = completion.take().get()
                ordered[index] = result
            }
            return ordered.toList()
        } finally {
            executor.shutdownNow()
            try {
                executor.awaitTermination(2, TimeUnit.SECONDS)
            } catch (_: InterruptedException) {
                Thread.currentThread().interrupt()
            }
        }
    }

    private fun ensureServerLocked() {
        if (commandServer != null) return
        DefaultNetworkMonitor.start()
        val cfPlatform = CFDataPlatformInterface()
        platform = cfPlatform
        val server = CommandServer(handler, cfPlatform)
        server.start()
        commandServer = server
    }

    private fun noSystemRoutingOptions(): OverrideOptions = OverrideOptions().apply {
        autoRedirect = false
    }

    private fun runSyncAttempt(config: String) {
        synchronized(lifecycleLock) {
            ensureServerLocked()
            commandServer!!.startOrReloadService(config, noSystemRoutingOptions())
            started = true
            testConfigDigest = null
        }
    }

    private fun ensureTestConfig(nodes: JSONArray) {
        val config = buildTestConfig(nodes)
        val digest = sha256(config)
        synchronized(lifecycleLock) {
            ensureServerLocked()
            if (!started || testConfigDigest != digest) {
                commandServer!!.startOrReloadService(config, noSystemRoutingOptions())
                started = true
                testConfigDigest = digest
            }
        }
    }

    private fun testLatencyWithVariants(
        server: CommandServer,
        node: JSONObject,
        repeat: Int,
        timeoutSeconds: Int,
    ): JSONObject {
        val base = baseNodeResult(node).apply {
            put("totalAttempts", repeat)
            put("mode", CORE_MODE)
        }
        val candidates = candidateTags(node)
        if (candidates.isEmpty()) {
            return base.apply {
                put("success", false)
                put("successCount", 0)
                put("lossRate", 100.0)
                put("error", "节点 outboundTag 为空")
            }
        }

        val errors = mutableListOf<String>()
        for ((index, candidate) in candidates.withIndex()) {
            try {
                val raw = server.cfDataTrueLatencyTest(candidate, TRACE_URL, repeat.toInt(), timeoutSeconds.toInt())
                val core = JSONObject(raw)
                copyAll(base, core)
                base.put("nodeId", node.optString("id"))
                base.put("node", node.optString("name", node.optString("id")))
                base.put("protocol", node.optString("protocol"))
                base.put("server", node.optString("server"))
                base.put("port", node.optInt("port"))
                base.put("testedOutboundTag", candidate)
                base.put("variantAttempts", index + 1)
                putWorkingSource(base, node, candidate)
                if (core.optBoolean("success")) return base
                val error = core.optString("error").trim()
                if (error.isNotBlank()) errors += "$candidate: $error"
            } catch (e: Exception) {
                errors += "$candidate: ${e.message ?: e}"
            }
        }

        return base.apply {
            put("success", false)
            put("successCount", 0)
            put("lossRate", 100.0)
            put("variantAttempts", candidates.size)
            put("error", if (errors.isEmpty()) "所有可用 outbound 均测试失败" else errors.joinToString("；"))
            put("testedOutboundTag", "")
        }
    }

    private fun testSpeedExactOutbound(
        server: CommandServer,
        node: JSONObject,
        durationSeconds: Int,
        downloadUrl: String,
    ): JSONObject {
        val outboundTag = node.optString("testedOutboundTag").trim()
            .ifBlank { node.optString("outboundTag").trim() }
        val result = baseNodeResult(node).apply {
            put("mode", CORE_MODE)
            put("testedOutboundTag", outboundTag)
            put("speedDurationSeconds", durationSeconds)
            put("speedUrl", downloadUrl)
        }
        if (outboundTag.isBlank()) {
            return result.apply {
                put("success", false)
                put("error", "没有 working outbound")
            }
        }
        return try {
            val raw = server.cfDataTrueSpeedTest(outboundTag, downloadUrl, durationSeconds.toInt())
            val speed = JSONObject(raw)
            copyAll(result, speed)
            result.put("nodeId", node.optString("id"))
            result.put("node", node.optString("name", node.optString("id")))
            result.put("protocol", node.optString("protocol"))
            result.put("server", node.optString("server"))
            result.put("port", node.optInt("port"))
            result.put("testedOutboundTag", outboundTag)
            result
        } catch (e: Exception) {
            result.apply {
                put("success", false)
                put("error", e.message ?: e.toString())
            }
        }
    }

    private fun baseNodeResult(node: JSONObject): JSONObject = JSONObject().apply {
        put("nodeId", node.optString("id"))
        put("node", node.optString("name", node.optString("id")))
        put("protocol", node.optString("protocol"))
        put("server", node.optString("server"))
        put("port", node.optInt("port"))
    }

    private fun candidateTags(node: JSONObject): List<String> {
        val result = mutableListOf<String>()
        val seen = HashSet<String>()
        fun add(tag: String) {
            val value = tag.trim()
            if (value.isNotEmpty() && seen.add(value)) result += value
        }
        add(node.optString("outboundTag"))
        val variants = node.optJSONArray("variants")
        if (variants != null) {
            for (index in 0 until variants.length()) {
                add(variants.optJSONObject(index)?.optString("outboundTag").orEmpty())
            }
        }
        return result
    }

    private fun putWorkingSource(result: JSONObject, node: JSONObject, candidate: String) {
        val rootSources = node.optJSONArray("sources")
        if (candidate == node.optString("outboundTag") && rootSources != null && rootSources.length() > 0) {
            rootSources.optJSONObject(0)?.let { putSourceText(result, it) }
            return
        }
        val variants = node.optJSONArray("variants") ?: return
        for (index in 0 until variants.length()) {
            val variant = variants.optJSONObject(index) ?: continue
            if (variant.optString("outboundTag") != candidate) continue
            val sources = variant.optJSONArray("sources")
            if (sources != null && sources.length() > 0) putSourceText(result, sources.optJSONObject(0))
            return
        }
    }

    private fun putSourceText(result: JSONObject, source: JSONObject?) {
        if (source == null) return
        val sourceName = source.optString("subscriptionName").trim()
        val nodeTag = source.optString("nodeTag").trim()
        val working = when {
            sourceName.isNotEmpty() && nodeTag.isNotEmpty() -> "$sourceName / $nodeTag"
            sourceName.isNotEmpty() -> sourceName
            else -> nodeTag
        }
        if (working.isNotEmpty()) result.put("workingSource", working)
    }

    private fun buildTestConfig(nodes: JSONArray): String {
        val outbounds = JSONArray()
        val seen = HashSet<String>()
        outbounds.put(JSONObject().apply {
            put("tag", "direct")
            put("type", "direct")
        })
        seen += "direct"

        fun addOutbound(outbound: JSONObject?, fallbackTag: String) {
            if (outbound == null) return
            val copy = JSONObject(outbound.toString())
            val tag = fallbackTag.trim().ifBlank { copy.optString("tag").trim() }
            if (tag.isBlank() || !seen.add(tag)) return
            copy.put("tag", tag)
            outbounds.put(copy)
        }

        for (index in 0 until nodes.length()) {
            val node = nodes.optJSONObject(index) ?: continue
            addOutbound(node.optJSONObject("outbound"), node.optString("outboundTag"))
            val variants = node.optJSONArray("variants")
            if (variants != null) {
                for (variantIndex in 0 until variants.length()) {
                    val variant = variants.optJSONObject(variantIndex) ?: continue
                    addOutbound(variant.optJSONObject("outbound"), variant.optString("outboundTag"))
                }
            }
        }

        if (outbounds.length() <= 1) throw IllegalArgumentException("节点缺少可用 outbound")
        return JSONObject().apply {
            put("log", JSONObject().apply {
                put("disabled", false)
                put("level", "warn")
                put("timestamp", true)
            })
            put("outbounds", outbounds)
            put("route", JSONObject().apply {
                put("final", "direct")
                put("auto_detect_interface", true)
            })
        }.toString(2)
    }

    private fun stageProviderConfig(configText: String): String {
        val root = JSONObject(configText)
        val providers = root.optJSONArray("providers")
            ?: throw IllegalArgumentException("Provider Core 配置缺少 providers")
        if (providers.length() == 0) {
            throw IllegalArgumentException("Provider Core 配置没有 provider")
        }
        val provider = providers.optJSONObject(0)
            ?: throw IllegalArgumentException("Provider Core 配置的 provider 无效")
        val path = provider.optString("path").trim()
        if (path.isBlank()) {
            throw IllegalArgumentException("Provider Core 配置缺少 provider path")
        }
        provider.put("path", path + ".next")
        return root.toString(2)
    }

    private fun deleteStagedProvider(path: String) {
        if (path.isBlank()) return
        val file = File(path)
        if (file.exists() && !file.delete()) {
            throw IllegalStateException("无法删除上一次 Provider 暂存文件：${file.absolutePath}")
        }
    }

    private fun promoteStagedProvider(stagedPath: String, providerPath: String) {
        val staged = File(stagedPath)
        val target = File(providerPath)
        if (!staged.isFile) {
            throw IllegalStateException("暂存 Provider 文件不存在：$stagedPath")
        }
        val parent = target.parentFile
        if (parent != null && !parent.exists() && !parent.mkdirs()) {
            throw IllegalStateException("无法创建 Provider 目录：${parent.absolutePath}")
        }

        val backup = File(target.parentFile ?: File("."), target.name + ".bak")
        if (backup.exists() && !backup.delete()) {
            throw IllegalStateException("无法清理 Provider 备份：${backup.absolutePath}")
        }

        var movedOld = false
        if (target.exists()) {
            if (!target.renameTo(backup)) {
                throw IllegalStateException("无法保护上一份 Provider1.json，已停止替换")
            }
            movedOld = true
        }

        if (!staged.renameTo(target)) {
            if (movedOld && !backup.renameTo(target)) {
                throw IllegalStateException(
                    "无法将新 Provider1.json 替换到正式路径，且旧缓存恢复失败；备份仍保留：${backup.absolutePath}",
                )
            }
            throw IllegalStateException("无法将新 Provider1.json 替换到正式路径")
        }
        if (movedOld && backup.exists() && !backup.delete()) {
            throw IllegalStateException("新 Provider1.json 已替换成功，但旧备份无法删除：${backup.absolutePath}")
        }
    }

    private fun waitForProviderNodes(path: String, timeoutSeconds: Int): Int {
        if (path.isBlank()) throw IllegalArgumentException("Provider 路径为空")
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(timeoutSeconds.toLong())
        var lastError: String? = null
        while (System.nanoTime() < deadline) {
            val file = File(path)
            if (file.isFile) {
                try {
                    val objectValue = JSONObject(stripProviderInfoHeader(file.readText()))
                    val count = arrayLength(objectValue, "outbounds") +
                        arrayLength(objectValue, "proxies") +
                        arrayLength(objectValue, "nodes")
                    if (count > 0) return count
                    lastError = "Provider1.json 存在但为空"
                } catch (e: Exception) {
                    lastError = e.message
                }
            }
            Thread.sleep(100)
        }
        if (!lastError.isNullOrBlank()) android.util.Log.w("CFDataSingBox", lastError)
        return 0
    }

    private fun stripProviderInfoHeader(raw: String): String {
        val text = raw.removePrefix("\uFEFF")
        return if (text.startsWith("#")) {
            val newline = text.indexOf('\n')
            if (newline >= 0) text.substring(newline + 1) else ""
        } else text
    }

    private fun arrayLength(objectValue: JSONObject, key: String): Int =
        objectValue.optJSONArray(key)?.length() ?: 0

    private fun copyAll(target: JSONObject, source: JSONObject) {
        val keys = source.keys()
        while (keys.hasNext()) {
            val key = keys.next()
            target.put(key, source.get(key))
        }
    }

    private fun sha256(value: String): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(value.toByteArray(Charsets.UTF_8))
        return digest.joinToString("") { "%02x".format(it) }
    }

    @JvmStatic
    fun stop() {
        synchronized(lifecycleLock) {
            if (!started && commandServer == null) return
            runCatching { commandServer?.closeService() }
            runCatching { commandServer?.close() }
            commandServer = null
            platform = null
            started = false
            testConfigDigest = null
            runCatching { DefaultNetworkMonitor.stop() }
        }
    }
}
