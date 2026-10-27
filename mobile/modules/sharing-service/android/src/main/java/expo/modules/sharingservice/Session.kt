package expo.modules.sharingservice

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import okhttp3.Request
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.CountDownLatch

/**
 * Keeps one authenticated websocket to the laptop alive forever: finds the laptop
 * (last address first, mDNS only when that fails), connects, reconnects with
 * backoff, and reacts to network changes.
 */
class Session(private val ctx: Context, private val prefs: Prefs, private val listener: Listener) {
  interface Listener {
    fun onHello(proto: Int) {}
    fun onMessage(m: JSONObject) {}
    fun onConnected() {}
    fun onDisconnected() {}
    fun onNetwork() {}
  }

  private val discovery = Discovery(ctx)
  private val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
  private val wake = Object()

  @Volatile private var stopped = false

  @Volatile private var resetBackoff = false

  @Volatile private var ws: WebSocket? = null

  /** e.g. https://192.168.1.5:53317 while the socket is open. */
  @Volatile var base: String? = null
    private set
  private var thread: Thread? = null

  private val netCallback = object : ConnectivityManager.NetworkCallback() {
    override fun onAvailable(network: Network) {
      poke()
      listener.onNetwork()
    }
  }

  fun start() {
    stopped = false
    cm.registerDefaultNetworkCallback(netCallback)
    thread = Thread(::loop, "sharing-session").also {
      it.isDaemon = true
      it.start()
    }
  }

  fun stop() {
    stopped = true
    try {
      cm.unregisterNetworkCallback(netCallback)
    } catch (_: Exception) {}
    ws?.cancel()
    poke()
  }

  /** Ends the current wait and restarts the search from a one second backoff (network change, Start button, a share). */
  fun poke(reset: Boolean = true) = synchronized(wake) {
    if (reset) resetBackoff = true
    wake.notifyAll()
  }

  fun send(text: String): Boolean = ws?.send(text) ?: false

  /** Where HTTP calls go: the live socket's address, else the last one that worked. */
  fun target(): String? = base ?: prefs.lastHost?.let { "https://${hostPart(it)}:${prefs.lastPort}" }

  private fun sleep(ms: Long) = synchronized(wake) { if (!stopped && !resetBackoff) wake.wait(ms) }

  private fun loop() {
    var backoff = MIN_BACKOFF
    while (!stopped) {
      val fp = prefs.fingerprint
      val token = prefs.peerToken
      val laptopId = prefs.laptopId
      if (fp == null || token == null || laptopId == null) {
        sleep(5_000)
        continue
      }
      val client = Net.client(fp)

      var connectedOnce = false
      val last = prefs.lastHost?.let { it to prefs.lastPort }
      // The last address first; mDNS (radio time and battery) only when it does not answer.
      if (last != null && session(client, last.first, last.second, token)) connectedOnce = true
      if (!connectedOnce && !stopped) {
        discovery.find(laptopId, 8_000)?.takeIf { it != last }?.let { (h, p) -> if (session(client, h, p, token)) connectedOnce = true }
      }
      if (stopped) return
      // A connection that lasted resets the backoff; repeated misses stretch it to MAX_BACKOFF.
      if (connectedOnce || resetBackoff) {
        backoff = MIN_BACKOFF
        resetBackoff = false
      } else {
        backoff = (backoff * 2).coerceAtMost(MAX_BACKOFF)
      }
      Status.connected = false
      Status.changed()
      listener.onDisconnected()
      sleep(backoff)
      if (resetBackoff) {
        backoff = MIN_BACKOFF
        resetBackoff = false
      }
    }
  }

  private fun hostPart(host: String) = if (host.contains(':')) "[${host.substringBefore('%')}]" else host

  /** Runs one websocket session; returns true if it ever opened. */
  private fun session(client: okhttp3.OkHttpClient, host: String, port: Int, token: String): Boolean {
    val url = "https://${hostPart(host)}:$port"
    val req = Request.Builder().url("$url/ws").header("Authorization", "Bearer $token").header("X-Sharing-Proto", "2").build()
    val closed = CountDownLatch(1)
    var opened = false
    ws = client.newWebSocket(
      req,
      object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: okhttp3.Response) {
          opened = true
          base = url
          prefs.lastHost = host
          prefs.lastPort = port
          Status.connected = true
          Status.lastError = null
          Status.changed()
          listener.onConnected()
        }
        override fun onMessage(webSocket: WebSocket, text: String) {
          val m = try {
            JSONObject(text)
          } catch (_: Exception) {
            return
          }
          if (m.optString("type") == "hello") {
            val proto = m.optInt("proto", 1)
            prefs.peerProto = proto
            listener.onHello(proto)
          }
          listener.onMessage(m)
        }
        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = closed.countDown()
        override fun onFailure(webSocket: WebSocket, t: Throwable, response: okhttp3.Response?) {
          if (response?.code == 401) {
            Status.lastError = "Laptop no longer trusts this phone; pair again"
            Status.changed()
          }
          closed.countDown()
        }
      },
    )
    closed.await()
    base = null
    return opened
  }

  companion object {
    const val MIN_BACKOFF = 1_000L
    const val MAX_BACKOFF = 5 * 60_000L
  }
}
