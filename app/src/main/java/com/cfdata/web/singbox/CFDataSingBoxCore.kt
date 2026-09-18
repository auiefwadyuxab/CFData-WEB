package com.cfdata.web.singbox

import io.nekohasekai.libbox.CommandServer
import io.nekohasekai.libbox.CommandServerHandler
import io.nekohasekai.libbox.OverrideOptions
import io.nekohasekai.libbox.PlatformInterface
import io.nekohasekai.libbox.SystemProxyStatus
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.concurrent.TimeUnit

object CFDataSingBoxCore {
    private const val DEFAULT_REPEAT = 3
    private const val DEFAULT_TIMEOUT_SECONDS = 8
    private const val DEFAULT_DOWNLOAD_BYTES = 10L * 1024L * 1024L
    private const val DEFAULT_DOWNLOAD_URL = "https://speed.cloudflare.com/__down?bytes=99999999"
    private const val TRACE_URL = "https://speed.cloudflare.com/cdn-cgi/trace"

    private val lifecycleLock = Any()
    private val operationLock = Any()
    private var platform: PlatformInterface? = null
    private var commandServer: CommandServer? = null
    private var started = false

    private val handler = object : CommandServerHandler {
        override fun serviceStop() {
            stop()
        }

        override fun serviceReload() {
            throw IllegalStateException("CFData controls sing-box configuration directly")
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
                OverrideOptions().apply { autoRedirect = false },
            )
            started = true
        }
    }

    fun syncProviders(payload: String): String = synchronized(operationLock) {
        val request = JSONObject(payload)
        val configs = request.optJSONArray("configs") ?: JSONArray()
        val syncTimeoutSeconds = request.optInt("timeoutSeconds", 90).coerceIn(1, 30)
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
                // ProviderRemote writes this path synchronously during StartContext.
                // Delete first so an older successful cache can never mask a new failure.
                if (providerPath.isNotBlank()) {
                    File(providerPath).delete()
                }
                runSyncAttempt(configText)
                var count = waitForProviderNodes(providerPath, syncTimeoutSeconds)
                if (count == 0) {
                    val fallback = clearProviderExcludes(configText)
                    if (fallback != null && fallback != configText) {
                        if (providerPath.isNotBlank()) {
                            File(providerPath).delete()
                        }
                        runSyncAttempt(fallback)
                        configText = fallback
                        count = waitForProviderNodes(providerPath, syncTimeoutSeconds)
                    }
                }
                if (count <= 0) {
                    throw IllegalStateException("Provider1.json 未生成可识别节点")
                }
                result.put("success", true)
                result.put("nodeCount", count)
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
        }.toString()
    }

    fun trueTest(payload: String): String = synchronized(operationLock) {
        val request = JSONObject(payload)
        val nodes = request.optJSONArray("nodes") ?: JSONArray()
        if (nodes.length() == 0) {
            throw IllegalArgumentException("没有可测试节点")
        }

        val repeat = request.optInt("repeat", DEFAULT_REPEAT).coerceIn(1, 10)
        val timeoutSeconds = request.optInt("timeout", DEFAULT_TIMEOUT_SECONDS).coerceIn(1, 60)
        val downloadBytes = request.optLong("downloadBytes", DEFAULT_DOWNLOAD_BYTES).coerceAtLeast(0)
        val downloadUrl = request.optString("downloadUrl", DEFAULT_DOWNLOAD_URL).trim()
            .ifBlank { DEFAULT_DOWNLOAD_URL }
        val config = buildTestConfig(nodes)

        synchronized(lifecycleLock) {
            ensureServerLocked()
            commandServer!!.startOrReloadService(
                config,
                OverrideOptions().apply { autoRedirect = false },
            )
            started = true
        }

        val results = JSONArray()
        var passed = 0
        for (index in 0 until nodes.length()) {
            val node = nodes.optJSONObject(index) ?: continue
            val result = testNode(
                commandServer!!,
                node,
                repeat,
                timeoutSeconds,
                downloadBytes,
                downloadUrl,
            )
            if (result.optBoolean("success")) {
                passed++
            }
            results.put(result)
        }

        JSONObject().apply {
            put("success", passed == results.length())
            put("total", results.length())
            put("passed", passed)
            put("results", results)
            put("mode", "libbox-in-process-direct-outbound")
        }.toString()
    }

    private fun ensureServerLocked() {
        if (commandServer != null) return
        DefaultNetworkMonitor.start()
        platform = CFDataPlatformInterface()
        val server = CommandServer(handler, platform)
        server.start()
        commandServer = server
    }

    private fun runSyncAttempt(config: String) {
        synchronized(lifecycleLock) {
            ensureServerLocked()
            commandServer!!.startOrReloadService(
                config,
                OverrideOptions().apply { autoRedirect = false },
            )
            started = true
        }
    }

    private fun waitForProviderNodes(path: String, timeoutSeconds: Int): Int {
        if (path.isBlank()) {
            throw IllegalArgumentException("Provider 路径为空")
        }
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
        if (!lastError.isNullOrBlank()) {
            android.util.Log.w("CFDataSingBox", lastError!!)
        }
        return 0
    }

    private fun clearProviderExcludes(configText: String): String? {
        return try {
            val root = JSONObject(configText)
            val providers = root.optJSONArray("providers") ?: return null
            var changed = false
            for (index in 0 until providers.length()) {
                val provider = providers.optJSONObject(index) ?: continue
                if (provider.has("exclude") && provider.optString("exclude") != "") {
                    provider.put("exclude", "")
                    changed = true
                }
            }
            if (changed) root.toString(2) else configText
        } catch (_: Exception) {
            null
        }
    }

    private fun buildTestConfig(nodes: JSONArray): String {
        val outbounds = JSONArray()
        val seen = HashSet<String>()
        outbounds.put(JSONObject().apply {
            put("tag", "direct")
            put("type", "direct")
        })

        fun addOutbound(outbound: JSONObject?, fallbackTag: String) {
            if (outbound == null) return
            val tag = fallbackTag.ifBlank { outbound.optString("tag").trim() }
            if (tag.isBlank() || !seen.add(tag)) return
            outbound.put("tag", tag)
            outbounds.put(outbound)
        }

        for (index in 0 until nodes.length()) {
            val node = nodes.optJSONObject(index) ?: continue
            addOutbound(node.optJSONObject("outbound"), node.optString("outboundTag").trim())
            val variants = node.optJSONArray("variants")
            if (variants != null) {
                for (variantIndex in 0 until variants.length()) {
                    val variant = variants.optJSONObject(variantIndex) ?: continue
                    addOutbound(
                        variant.optJSONObject("outbound"),
                        variant.optString("outboundTag").trim(),
                    )
                }
            }
        }

        if (outbounds.length() <= 1) {
            throw IllegalArgumentException("节点缺少可用 outbound")
        }

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

    private fun testNode(
        server: CommandServer,
        node: JSONObject,
        repeat: Int,
        timeoutSeconds: Int,
        downloadBytes: Long,
        downloadUrl: String,
    ): JSONObject {
        val nodeId = node.optString("id")
        val result = JSONObject().apply {
            put("nodeId", nodeId)
            put("node", node.optString("name", nodeId))
            put("protocol", node.optString("protocol"))
            put("server", node.optString("server"))
            put("port", node.optInt("port"))
            put("totalAttempts", repeat)
            put("mode", "libbox-in-process-direct-outbound")
        }

        val outboundTag = node.optString("outboundTag").trim()
        if (outboundTag.isBlank()) {
            result.put("success", false)
            result.put("successCount", 0)
            result.put("lossRate", 100.0)
            result.put("error", "节点 outboundTag 为空")
            return result
        }

        val candidates = JSONArray().apply {
            put(JSONObject().apply {
                put("outboundTag", outboundTag)
                put("sources", node.optJSONArray("sources") ?: JSONArray())
            })
            val variants = node.optJSONArray("variants")
            if (variants != null) {
                for (index in 0 until variants.length()) {
                    val variant = variants.optJSONObject(index) ?: continue
                    val tag = variant.optString("outboundTag").trim()
                    if (tag.isBlank() || tag == outboundTag) continue
                    put(JSONObject().apply {
                        put("outboundTag", tag)
                        put("sources", variant.optJSONArray("sources") ?: JSONArray())
                    })
                }
            }
        }

        val errors = mutableListOf<String>()
        for (index in 0 until candidates.length()) {
            val candidate = candidates.optJSONObject(index) ?: continue
            val candidateTag = candidate.optString("outboundTag").trim()
            if (candidateTag.isBlank()) continue
            try {
                val raw = server.cfDataTrueTest(
                    candidateTag,
                    TRACE_URL,
                    downloadUrl,
                    repeat,
                    timeoutSeconds,
                    downloadBytes,
                )
                val coreResult = JSONObject(raw)
                result.remove("error")
                val keys = coreResult.keys()
                while (keys.hasNext()) {
                    val key = keys.next()
                    result.put(key, coreResult.get(key))
                }
                result.put("testedOutboundTag", candidateTag)
                result.put("variantAttempts", index + 1)
                val sources = candidate.optJSONArray("sources")
                if (sources != null && sources.length() > 0) {
                    val firstSource = sources.optJSONObject(0)
                    val sourceName = firstSource?.optString("subscriptionName").orEmpty()
                    val nodeTag = firstSource?.optString("nodeTag").orEmpty()
                    val workingSource = when {
                        sourceName.isNotBlank() && nodeTag.isNotBlank() -> "$sourceName / $nodeTag"
                        sourceName.isNotBlank() -> sourceName
                        nodeTag.isNotBlank() -> nodeTag
                        else -> ""
                    }
                    if (workingSource.isNotBlank()) result.put("workingSource", workingSource)
                }
                if (coreResult.optBoolean("success")) {
                    return result
                }
                coreResult.optString("error").takeIf { it.isNotBlank() }?.let { errors += "$candidateTag: $it" }
            } catch (e: Exception) {
                errors += "$candidateTag: ${e.message ?: e}"
            }
        }

        result.put("success", false)
        result.put("successCount", 0)
        result.put("lossRate", 100.0)
        result.put("variantAttempts", candidates.length())
        result.put("error", if (errors.isEmpty()) "所有可用 outbound 均测试失败" else errors.joinToString("；"))
        return result
    }

    private fun stripProviderInfoHeader(raw: String): String {
        val text = raw.removePrefix("\uFEFF")
        return if (text.startsWith("#")) {
            val newline = text.indexOf('\n')
            if (newline >= 0) text.substring(newline + 1) else ""
        } else {
            text
        }
    }

    private fun arrayLength(objectValue: JSONObject, key: String): Int =
        objectValue.optJSONArray(key)?.length() ?: 0

    fun stop() {
        synchronized(lifecycleLock) {
            if (!started && commandServer == null) return
            runCatching { commandServer?.closeService() }
            runCatching { commandServer?.close() }
            commandServer = null
            platform = null
            started = false
            runCatching { DefaultNetworkMonitor.stop() }
        }
    }
}
