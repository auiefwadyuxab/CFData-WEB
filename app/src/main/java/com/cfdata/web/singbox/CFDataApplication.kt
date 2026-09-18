package com.cfdata.web.singbox

import android.app.Application
import android.os.Build
import com.cfdata.web.BuildConfig
import io.nekohasekai.libbox.Libbox
import org.json.JSONObject
import io.nekohasekai.libbox.SetupOptions
import java.io.File

class CFDataApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        INSTANCE = this
        val dataDir = filesDir
        val workingDir = File(dataDir, "singbox_subscriptions").apply { mkdirs() }
        val tempDir = cacheDir.apply { mkdirs() }
        val options = SetupOptions().apply {
            basePath = dataDir.absolutePath
            workingPath = workingDir.absolutePath
            tempPath = tempDir.absolutePath
            fixAndroidStack = Build.VERSION.SDK_INT >= 29
            logMaxLines = 3000
            debug = false
            crashReportSource = "CFData"
            appVersion = BuildConfig.VERSION_CODE.toString()
            appMarketingVersion = BuildConfig.VERSION_NAME
            oomKillerEnabled = false
            oomKillerDisabled = true
            oomMemoryLimit = 0
            powerReportEnabled = false
            platformMetadata = JSONObject().apply {
                put("os", "Android ${Build.VERSION.RELEASE}")
                put("sdk", Build.VERSION.SDK_INT)
                put("manufacturer", Build.MANUFACTURER)
                put("model", Build.MODEL)
            }.toString()
        }
        Libbox.setup(options)
    }

    companion object {
        lateinit var INSTANCE: CFDataApplication
            private set
    }
}
