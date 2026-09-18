package com.cfdata.web.singbox

import android.net.DnsResolver
import android.os.Build
import android.os.CancellationSignal
import android.system.ErrnoException
import io.nekohasekai.libbox.ExchangeContext
import io.nekohasekai.libbox.LocalDNSTransport
import java.net.InetAddress
import java.net.UnknownHostException
import java.util.concurrent.CancellationException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors

object LocalResolver : LocalDNSTransport {
    private const val RCODE_NXDOMAIN = 3
    private val executor = Executors.newCachedThreadPool { runnable ->
        Thread(runnable, "cfdata-libbox-dns").apply { isDaemon = true }
    }

    override fun raw(): Boolean = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q

    override fun exchange(ctx: ExchangeContext, message: ByteArray) {
        val network = DefaultNetworkMonitor.defaultNetwork ?: error("missing default network")
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            throw UnsupportedOperationException("raw DNS transport requires Android 10+")
        }
        val latch = CountDownLatch(1)
        val signal = CancellationSignal()
        ctx.onCancel {
            signal.cancel()
            latch.countDown()
        }
        try {
            DnsResolver.getInstance().rawQuery(
                network,
                message,
                DnsResolver.FLAG_NO_RETRY,
                executor,
                signal,
                object : DnsResolver.Callback<ByteArray> {
                    override fun onAnswer(answer: ByteArray, rcode: Int) {
                        if (rcode == 0) ctx.rawSuccess(answer) else ctx.errorCode(rcode)
                        latch.countDown()
                    }

                    override fun onError(error: DnsResolver.DnsException) {
                        val cause = error.cause
                        if (cause is ErrnoException) {
                            ctx.errnoCode(cause.errno)
                        } else {
                            ctx.errorCode(RCODE_NXDOMAIN)
                        }
                        latch.countDown()
                    }
                },
            )
            latch.await()
        } catch (_: CancellationException) {
            return
        } catch (e: Exception) {
            throw e
        }
    }

    override fun lookup(ctx: ExchangeContext, network: String, domain: String) {
        val defaultNetwork = DefaultNetworkMonitor.defaultNetwork ?: error("missing default network")
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            val answer = try {
                defaultNetwork.getAllByName(domain)
            } catch (_: UnknownHostException) {
                ctx.errorCode(RCODE_NXDOMAIN)
                return
            }
            ctx.success(answer.mapNotNull(InetAddress::getHostAddress).joinToString("\n"))
            return
        }

        val latch = CountDownLatch(1)
        val signal = CancellationSignal()
        ctx.onCancel {
            signal.cancel()
            latch.countDown()
        }
        val callback = object : DnsResolver.Callback<Collection<InetAddress>> {
            override fun onAnswer(answer: Collection<InetAddress>, rcode: Int) {
                if (rcode == 0) {
                    ctx.success(answer.mapNotNull { it.hostAddress }.joinToString("\n"))
                } else {
                    ctx.errorCode(rcode)
                }
                latch.countDown()
            }

            override fun onError(error: DnsResolver.DnsException) {
                val cause = error.cause
                if (cause is ErrnoException) {
                    ctx.errnoCode(cause.errno)
                } else {
                    ctx.errorCode(RCODE_NXDOMAIN)
                }
                latch.countDown()
            }
        }
        val type = when {
            network.endsWith("4") -> DnsResolver.TYPE_A
            network.endsWith("6") -> DnsResolver.TYPE_AAAA
            else -> null
        }
        try {
            if (type != null) {
                DnsResolver.getInstance().query(
                    defaultNetwork,
                    domain,
                    type,
                    DnsResolver.FLAG_NO_RETRY,
                    executor,
                    signal,
                    callback,
                )
            } else {
                DnsResolver.getInstance().query(
                    defaultNetwork,
                    domain,
                    DnsResolver.FLAG_NO_RETRY,
                    executor,
                    signal,
                    callback,
                )
            }
            latch.await()
        } catch (e: Exception) {
            throw e
        }
    }
}
