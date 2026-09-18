package com.cfdata.web.singbox

import android.annotation.TargetApi
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.Handler
import android.os.Looper
import io.nekohasekai.libbox.InterfaceUpdateListener
import java.net.NetworkInterface
import java.util.concurrent.atomic.AtomicReference

object DefaultNetworkMonitor {
    private data class State(val network: Network? = null, val linkProperties: LinkProperties? = null)

    @Volatile
    var defaultNetwork: Network? = null
        private set

    private val state = AtomicReference(State())
    @Volatile private var listener: InterfaceUpdateListener? = null
    @Volatile private var started = false
    @Volatile private var fallback = false

    private val connectivity: ConnectivityManager
        get() = CFDataApplication.INSTANCE.getSystemService(ConnectivityManager::class.java)

    private val request = NetworkRequest.Builder().apply {
        addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
        addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED)
        if (Build.VERSION.SDK_INT == 23) {
            removeCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
            removeCapability(NetworkCapabilities.NET_CAPABILITY_CAPTIVE_PORTAL)
        }
    }.build()

    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            update(network, null)
        }

        override fun onCapabilitiesChanged(network: Network, networkCapabilities: NetworkCapabilities) {
            val current = state.get()
            if (current.network == network) update(network, current.linkProperties)
        }

        override fun onLinkPropertiesChanged(network: Network, linkProperties: LinkProperties) {
            update(network, linkProperties)
        }

        override fun onLost(network: Network) {
            val current = state.get()
            if (current.network == network) {
                state.set(State())
                defaultNetwork = null
                notifyListener(null, null)
            }
        }
    }

    @Synchronized
    fun start() {
        if (started) return
        started = true
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            val current = connectivity.activeNetwork
            state.set(State(current, current?.let(connectivity::getLinkProperties)))
            defaultNetwork = current
        }
        register()
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M && state.get().network == null) {
            val current = connectivity.activeNetwork
            state.set(State(current, current?.let(connectivity::getLinkProperties)))
            defaultNetwork = current
        }
        notifyListener(state.get().network, state.get().linkProperties)
    }

    @Synchronized
    fun stop() {
        if (!started) return
        runCatching { connectivity.unregisterNetworkCallback(callback) }
        started = false
        listener = null
        state.set(State())
        defaultNetwork = null
    }

    fun setListener(newListener: InterfaceUpdateListener?) {
        listener = newListener
        if (newListener != null && !started) start()
        val current = state.get()
        notifyListener(current.network, current.linkProperties)
    }

    private fun update(network: Network, linkProperties: LinkProperties?) {
        val properties = linkProperties ?: connectivity.getLinkProperties(network)
        state.set(State(network, properties))
        defaultNetwork = network
        notifyListener(network, properties)
    }

    private fun notifyListener(network: Network?, linkProperties: LinkProperties?) {
        val currentListener = listener ?: return
        if (network == null) {
            currentListener.updateDefaultInterface("", -1, false, false)
            return
        }
        val interfaceName = linkProperties?.interfaceName ?: return
        repeat(10) {
            try {
                val index = NetworkInterface.getByName(interfaceName).index
                currentListener.updateDefaultInterface(interfaceName, index, false, false)
                return
            } catch (_: Exception) {
                Thread.sleep(100)
            }
        }
    }

    @Suppress("DEPRECATION")
    private fun register() {
        val manager = connectivity
        val mainHandler = Handler(Looper.getMainLooper())
        when {
            Build.VERSION.SDK_INT >= 31 -> {
                @TargetApi(31)
                manager.registerBestMatchingNetworkCallback(request, callback, mainHandler)
            }
            Build.VERSION.SDK_INT >= 28 -> {
                @TargetApi(28)
                manager.requestNetwork(request, callback, mainHandler)
            }
            Build.VERSION.SDK_INT >= 26 -> {
                @TargetApi(26)
                manager.registerDefaultNetworkCallback(callback, mainHandler)
            }
            Build.VERSION.SDK_INT >= 24 -> {
                manager.registerDefaultNetworkCallback(callback)
            }
            else -> {
                fallback = true
                runCatching { manager.requestNetwork(request, callback) }.onFailure {
                    state.set(State(manager.activeNetwork, manager.activeNetwork?.let(manager::getLinkProperties)))
                }
            }
        }
    }
}
