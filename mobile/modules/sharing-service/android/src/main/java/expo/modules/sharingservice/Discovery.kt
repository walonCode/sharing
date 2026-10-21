package expo.modules.sharingservice

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit

/** Finds the paired laptop on the LAN via mDNS (`_sharing._tcp`). */
class Discovery(ctx: Context) {
  private val nsd = ctx.applicationContext.getSystemService(Context.NSD_SERVICE) as NsdManager
  private val wifi = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager

  /** Blocks up to [timeoutMs]; returns host and port, or null if not seen. */
  fun find(laptopId: String, timeoutMs: Long): Pair<String, Int>? {
    val lock = wifi.createMulticastLock("sharing-mdns").apply { setReferenceCounted(false); acquire() }
    val result = CompletableFuture<Pair<String, Int>>()
    val listener = object : NsdManager.DiscoveryListener {
      override fun onServiceFound(info: NsdServiceInfo) {
        if (info.serviceName != laptopId || result.isDone) return
        @Suppress("DEPRECATION")
        nsd.resolveService(info, object : NsdManager.ResolveListener {
          override fun onResolveFailed(i: NsdServiceInfo, code: Int) {}
          override fun onServiceResolved(i: NsdServiceInfo) {
            @Suppress("DEPRECATION")
            val host = i.host?.hostAddress ?: return
            result.complete(host to i.port)
          }
        })
      }
      override fun onStartDiscoveryFailed(type: String, code: Int) { result.complete(null) }
      override fun onStopDiscoveryFailed(type: String, code: Int) {}
      override fun onDiscoveryStarted(type: String) {}
      override fun onDiscoveryStopped(type: String) {}
      override fun onServiceLost(info: NsdServiceInfo) {}
    }
    return try {
      nsd.discoverServices("_sharing._tcp", NsdManager.PROTOCOL_DNS_SD, listener)
      try { result.get(timeoutMs, TimeUnit.MILLISECONDS) } catch (_: Exception) { null }
    } finally {
      try { nsd.stopServiceDiscovery(listener) } catch (_: Exception) {}
      lock.release()
    }
  }
}
