package com.cfdata.web.singbox

import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import io.nekohasekai.libbox.AutoRedirectHandler
import io.nekohasekai.libbox.AutoRedirectSession
import io.nekohasekai.libbox.BridgeOptions
import io.nekohasekai.libbox.BridgeSession
import io.nekohasekai.libbox.ConnectionOwner
import io.nekohasekai.libbox.InterfaceUpdateListener
import io.nekohasekai.libbox.LocalDNSTransport
import io.nekohasekai.libbox.NetworkInterfaceIterator
import io.nekohasekai.libbox.Notification
import io.nekohasekai.libbox.NetworkInterface
import io.nekohasekai.libbox.NeighborUpdateListener
import io.nekohasekai.libbox.PlatformInterface
import io.nekohasekai.libbox.PlatformUser
import io.nekohasekai.libbox.ShellSession
import io.nekohasekai.libbox.StringIterator
import io.nekohasekai.libbox.TunOptions
import io.nekohasekai.libbox.WIFIState
import java.net.NetworkInterface as JNetworkInterface

class CFDataPlatformInterface : PlatformInterface {
    private val context get() = CFDataApplication.INSTANCE
    private val connectivity: ConnectivityManager get() = context.getSystemService(ConnectivityManager::class.java)

    override fun usePlatformAutoDetectInterfaceControl(): Boolean = true
    override fun autoDetectInterfaceControl(fd: Int) {}

    override fun openTun(options: TunOptions): Int = error("CFData standalone mode does not use Android TUN")

    override fun useProcFS(): Boolean = false

    override fun findConnectionOwner(
        ipProtocol: Int,
        sourceAddress: String,
        sourcePort: Int,
        destinationAddress: String,
        destinationPort: Int,
    ): ConnectionOwner = error("connection owner lookup is disabled in CFData standalone mode")

    override fun startDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        DefaultNetworkMonitor.setListener(listener)
    }

    override fun closeDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        DefaultNetworkMonitor.setListener(null)
    }

    override fun getInterfaces(): NetworkInterfaceIterator {
        val javaInterfaces = runCatching {
            val enumeration = JNetworkInterface.getNetworkInterfaces()
            if (enumeration == null) {
                emptyList()
            } else {
                buildList {
                    while (enumeration.hasMoreElements()) {
                        add(enumeration.nextElement())
                    }
                }
            }
        }.getOrElse { emptyList() }
        val result = mutableListOf<NetworkInterface>()
        val networks = connectivity.allNetworks
        for (network in networks) {
            val link = connectivity.getLinkProperties(network) ?: continue
            val caps = connectivity.getNetworkCapabilities(network) ?: continue
            val name = link.interfaceName ?: continue
            val javaInterface = javaInterfaces.firstOrNull { it.name == name } ?: continue
            val item = NetworkInterface()
            item.name = name
            item.index = javaInterface.index
            item.mtu = runCatching { javaInterface.mtu }.getOrDefault(1500)
            item.dnsServer = StringArray(link.dnsServers.mapNotNull { it.hostAddress }.iterator())
            item.gateway = StringArray(link.routes.mapNotNull { it.gateway?.hostAddress }.iterator())
            item.addresses = StringArray(javaInterface.interfaceAddresses.map { address ->
                "${address.address.hostAddress}/${address.networkPrefixLength}"
            }.iterator())
            item.type = when {
                caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> io.nekohasekai.libbox.Libbox.InterfaceTypeWIFI
                caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> io.nekohasekai.libbox.Libbox.InterfaceTypeCellular
                caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> io.nekohasekai.libbox.Libbox.InterfaceTypeEthernet
                else -> io.nekohasekai.libbox.Libbox.InterfaceTypeOther
            }
            item.metered = !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
            result.add(item)
        }
        return InterfaceArray(result.iterator())
    }

    override fun underNetworkExtension(): Boolean = false
    override fun includeAllNetworks(): Boolean = false
    override fun clearDNSCache() {}

    // reF1nd 1.15.0-alpha.6-reF1nd exposes notification callbacks in the
    // Libbox PlatformInterface. CFData is not a notification/VPN client, so
    // intentionally keep these hooks as no-ops while satisfying the ABI.
    override fun sendNotification(notification: Notification) {}

    override fun cancelNotification(identifier: String, typeID: Int) {}

    @Suppress("DEPRECATION")
    override fun readWIFIState(): WIFIState? {
        val wifi = context.getSystemService(android.net.wifi.WifiManager::class.java)?.connectionInfo ?: return null
        var ssid = wifi.ssid ?: ""
        if (ssid == "<unknown ssid>") ssid = ""
        if (ssid.startsWith("\"") && ssid.endsWith("\"")) ssid = ssid.substring(1, ssid.length - 1)
        return WIFIState(ssid, wifi.bssid ?: "")
    }

    override fun localDNSTransport(): LocalDNSTransport? = LocalResolver
    override fun startNeighborMonitor(listener: NeighborUpdateListener?) {}
    override fun closeNeighborMonitor(listener: NeighborUpdateListener?) {}

    override fun usePlatformShell(): Boolean = false
    override fun checkPlatformShell() {}
    override fun openShellSession(
        user: PlatformUser?,
        command: String?,
        environ: StringIterator?,
        term: String?,
        rows: Int,
        cols: Int,
    ): ShellSession = error("shell is disabled in CFData standalone mode")

    override fun readSystemSSHHostKey(): String = error("SSH host key is unavailable")
    override fun lookupSFTPServer(): String = error("SFTP is unavailable")
    override fun tailscaleHostname(): String = "CFData"

    override fun usePlatformBridge(): Boolean = false
    override fun createBridge(options: BridgeOptions?): BridgeSession = error("bridge is disabled in CFData standalone mode")

    override fun usePlatformAutoRedirect(): Boolean = false
    override fun createAutoRedirect(options: ByteArray?, handler: AutoRedirectHandler?): AutoRedirectSession = error("auto redirect is disabled in CFData standalone mode")

    override fun lookupUser(username: String?): PlatformUser = PlatformUser().apply {
        this.username = context.packageName
        this.uid = android.os.Process.myUid()
        this.gid = android.os.Process.myUid()
        this.homeDir = context.filesDir.absolutePath
    }

    override fun registerMyInterface(name: String?) {}

    private class InterfaceArray(private val iterator: Iterator<NetworkInterface>) : NetworkInterfaceIterator {
        override fun hasNext(): Boolean = iterator.hasNext()
        override fun next(): NetworkInterface = iterator.next()
    }

    class StringArray(private val iterator: Iterator<String>) : StringIterator {
        override fun len(): Int = 0
        override fun hasNext(): Boolean = iterator.hasNext()
        override fun next(): String = iterator.next()
    }
}
