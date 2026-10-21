package expo.modules.sharingservice

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.provider.OpenableColumns
import androidx.documentfile.provider.DocumentFile
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.BufferedSink
import org.json.JSONObject
import java.net.URLEncoder
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors

/**
 * Keeps one authenticated websocket to the laptop alive forever: finds the
 * laptop (last address, then mDNS), connects, reconnects with backoff, and
 * reacts to network changes. Also performs uploads and downloads.
 */
class Connection(private val ctx: Context, private val notifier: Notifier) {
  private val prefs = Prefs(ctx)
  private val discovery = Discovery(ctx)
  private val io = Executors.newCachedThreadPool()
  private val main = Handler(Looper.getMainLooper())
  private val clipboard = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
  private val wake = Object()

  @Volatile private var stopped = false
  @Volatile private var ws: WebSocket? = null
  @Volatile private var client: OkHttpClient? = null
  @Volatile private var base: String? = null // e.g. https://192.168.1.5:53317
  @Volatile private var lastClip = ""
  @Volatile private var lastClipTs = 0L
  private var thread: Thread? = null

  private val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
  private val netCallback = object : ConnectivityManager.NetworkCallback() {
    override fun onAvailable(network: Network) = poke()
  }

  fun start() {
    stopped = false
    cm.registerDefaultNetworkCallback(netCallback)
    thread = Thread(::loop, "sharing-connection").also { it.isDaemon = true; it.start() }
  }

  fun stop() {
    stopped = true
    try { cm.unregisterNetworkCallback(netCallback) } catch (_: Exception) {}
    ws?.cancel()
    poke()
    io.shutdownNow()
  }

  private fun poke() = synchronized(wake) { wake.notifyAll() }

  private fun sleep(ms: Long) = synchronized(wake) { if (!stopped) wake.wait(ms) }

  private fun loop() {
    var backoff = 1_000L
    while (!stopped) {
      val fp = prefs.fingerprint
      val token = prefs.peerToken
      val laptopId = prefs.laptopId
      if (fp == null || token == null || laptopId == null) { sleep(5_000); continue }
      client = Net.client(fp)

      val candidates = buildList {
        prefs.lastHost?.let { add(it to prefs.lastPort) }
        discovery.find(laptopId, 8_000)?.let { if (it !in this) add(it) }
      }
      var connectedOnce = false
      for ((host, port) in candidates) {
        if (stopped) return
        if (session(host, port, token)) { connectedOnce = true; break }
      }
      if (connectedOnce) backoff = 1_000L else backoff = (backoff * 2).coerceAtMost(30_000L)
      Status.connected = false
      Status.changed()
      sleep(backoff)
    }
  }

  private fun hostPart(host: String) = if (host.contains(':')) "[${host.substringBefore('%')}]" else host

  /** Runs one websocket session; returns true if it ever opened. */
  private fun session(host: String, port: Int, token: String): Boolean {
    val url = "https://${hostPart(host)}:$port"
    val req = Request.Builder().url("$url/ws").header("Authorization", "Bearer $token").build()
    val closed = CountDownLatch(1)
    var opened = false
    ws = client!!.newWebSocket(req, object : WebSocketListener() {
      override fun onOpen(webSocket: WebSocket, response: okhttp3.Response) {
        opened = true
        base = url
        prefs.lastHost = host
        prefs.lastPort = port
        Status.connected = true
        Status.lastError = null
        Status.changed()
      }
      override fun onMessage(webSocket: WebSocket, text: String) = handle(JSONObject(text))
      override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = closed.countDown()
      override fun onFailure(webSocket: WebSocket, t: Throwable, response: okhttp3.Response?) {
        if (response?.code == 401) { Status.lastError = "Laptop no longer trusts this phone; pair again"; Status.changed() }
        closed.countDown()
      }
    })
    closed.await()
    base = null
    return opened
  }

  // ---- incoming ----

  private fun handle(m: JSONObject) {
    when (m.optString("type")) {
      "offer" -> io.execute { download(m.getString("id"), m.getString("kind"), m.optString("name", "file")) }
      "clip" -> applyClip(m.optString("data"), m.optLong("ts"))
    }
  }

  private fun applyClip(data: String, ts: Long) {
    if (data.isEmpty() || data == lastClip || ts < lastClipTs) return
    lastClip = data
    lastClipTs = ts
    main.post { clipboard.setPrimaryClip(ClipData.newPlainText("sharing", data)) }
  }

  private fun download(id: String, kind: String, name: String) {
    val b = base ?: return
    try {
      val root = Storage.root(ctx, prefs)
      val req = Request.Builder().url("$b/transfer/$id").header("Authorization", "Bearer ${prefs.peerToken}").build()
      client!!.newCall(req).execute().use { resp ->
        if (!resp.isSuccessful) error("Laptop refused the transfer (${resp.code})")
        val body = resp.body!!.byteStream()
        val saved = if (kind == "dir") Storage.untar(root, name, body, ctx) else Storage.saveFile(root, name, body, ctx)
        notifier.transfer("Received", saved)
      }
    } catch (e: Exception) {
      notifier.transfer("Receive failed", "$name: ${e.message}")
    }
  }

  // ---- outgoing ----

  fun sendClip(text: String) {
    if (text.isEmpty() || text == lastClip) return
    lastClip = text
    lastClipTs = System.currentTimeMillis()
    val msg = JSONObject().put("type", "clip").put("mime", "text/plain").put("data", text).put("ts", lastClipTs)
    ws?.send(msg.toString())
  }

  private fun uploadTarget(): String? = base ?: prefs.lastHost?.let { "https://${hostPart(it)}:${prefs.lastPort}" }

  fun uploadUris(uris: List<Uri>) = uris.forEach { uri -> io.execute { uploadUri(uri) } }

  fun uploadTree(tree: Uri) = io.execute {
    val folder = DocumentFile.fromTreeUri(ctx, tree)
    if (folder == null) { notifier.transfer("Send failed", "Folder not readable"); return@execute }
    val name = folder.name ?: "folder"
    val body = object : RequestBody() {
      override fun contentType() = "application/x-tar".toMediaType()
      override fun writeTo(sink: BufferedSink) = Storage.tarTree(folder, ctx, sink.outputStream())
    }
    put("name=${enc(name)}&kind=dir", body, name)
  }

  private fun uploadUri(uri: Uri) {
    var name = "shared-file"
    var size = -1L
    ctx.contentResolver.query(uri, null, null, null, null)?.use { c ->
      if (c.moveToFirst()) {
        c.getColumnIndex(OpenableColumns.DISPLAY_NAME).takeIf { it >= 0 }?.let { name = c.getString(it) }
        c.getColumnIndex(OpenableColumns.SIZE).takeIf { it >= 0 && !c.isNull(it) }?.let { size = c.getLong(it) }
      }
    }
    val type = (ctx.contentResolver.getType(uri) ?: "application/octet-stream").toMediaType()
    val body = object : RequestBody() {
      override fun contentType() = type
      override fun contentLength() = size
      override fun writeTo(sink: BufferedSink) {
        ctx.contentResolver.openInputStream(uri)!!.use { sink.outputStream().let { out -> it.copyTo(out) } }
      }
    }
    put("name=${enc(name)}&kind=file", body, name)
  }

  private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")

  private fun put(query: String, body: RequestBody, label: String) {
    val url = uploadTarget()
    val c = client ?: prefs.fingerprint?.let { Net.client(it).also { n -> client = n } }
    if (url == null || c == null) { notifier.transfer("Send failed", "$label: laptop not reachable"); return }
    try {
      val req = Request.Builder().url("$url/upload?$query").header("Authorization", "Bearer ${prefs.peerToken}").put(body).build()
      c.newCall(req).execute().use { resp ->
        if (resp.isSuccessful) notifier.transfer("Sent", label) else notifier.transfer("Send failed", "$label (${resp.code})")
      }
    } catch (e: Exception) {
      notifier.transfer("Send failed", "$label: ${e.message}")
    }
  }
}
