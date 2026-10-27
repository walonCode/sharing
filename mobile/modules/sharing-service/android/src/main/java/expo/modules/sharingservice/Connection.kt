package expo.modules.sharingservice

import android.content.Context
import android.net.Uri
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.Executors

/**
 * Owns the pieces that talk to the laptop - the [Session] (websocket and
 * reconnect), the [Transfers] queue and [ClipSync] - and routes messages between
 * them. The service only starts and stops this object.
 */
class Connection(private val ctx: Context, private val prefs: Prefs, private val notifier: Notifier) : Session.Listener {
  private val store = SqliteTransferStore(ctx)
  private val spool = Spool(ctx, prefs.spoolThreshold)
  private val sources = AndroidSources(ctx, spool)
  private val misc = Executors.newSingleThreadExecutor { r -> Thread(r, "sharing-misc").also { it.isDaemon = true } }
  private val session = Session(ctx, prefs, this)
  private val clip = ClipSync(ctx) { session.send(it) }
  private var watcher: ClipWatcher? = null
  private var lastProgressSent = 0L

  private val events = object : TransferEvents {
    override fun progress(t: Transfer, bytes: Long) {
      transfers.checkCancelled(t.id)
      notifier.progress(t, bytes)
      val now = System.currentTimeMillis()
      if (t.direction == Dir.DOWN && now - lastProgressSent > 3_000) {
        lastProgressSent = now
        session.send(JSONObject().put("type", "progress").put("id", t.source).put("offset", bytes).toString())
      }
    }
    override fun changed(t: Transfer) = notifier.changed(t)
    override fun authFailed() {
      Status.lastError = "Laptop no longer trusts this phone; pair again"
      Status.changed()
      notifier.authFailed()
    }
  }

  private val transfers: Transfers = Transfers(
    store = store,
    attemptUp = { uploader.attempt(it) },
    attemptDown = { downloader.attempt(it) },
    discardDown = { downloader.discard(it) },
    events = events,
    remote = ::remote,
  )

  private val uploader = Uploader(
    remote = ::remote,
    store = store,
    sources = sources,
    onProgress = { t, b -> events.progress(t, b) },
    legacy = { prefs.peerProto < 2 },
    setLegacy = { prefs.peerProto = 1 },
  )

  private val downloader = Downloader(
    remote = ::remote,
    store = store,
    sink = { MediaStoreSink(ctx, prefs.laptopName ?: "laptop") },
    onProgress = { t, b -> events.progress(t, b) },
    legacyDir = ::legacyDirDownload,
  )

  init {
    transfers.onSpoolCleanup { spool.delete(it.id) }
  }

  /** A client for the laptop's current address, or null before the first successful connection. */
  private fun remote(): Remote? {
    val fp = prefs.fingerprint ?: return null
    val target = session.target() ?: return null
    return Remote(Net.client(fp), target, token = { prefs.peerToken })
  }

  fun start() {
    if (prefs.autoClipboard) watcher = ClipWatcher(ctx) { sendClip(it) }.also { it.start() }
    transfers.start()
    session.start()
    spool.sweep(store.all().map { it.id }.toSet())
  }

  fun stop() {
    watcher?.stop()
    session.stop()
    transfers.stop()
    misc.shutdownNow()
  }

  /** The user pressed Start or shared something: look for the laptop now. */
  fun poke() {
    session.poke()
    transfers.retryNow()
  }

  // ---- session events ----

  override fun onHello(proto: Int) {}

  override fun onConnected() {
    clip.onConnected()
    transfers.retryNow()
  }

  override fun onNetwork() = transfers.retryNow()

  override fun onMessage(m: JSONObject) {
    when (m.optString("type")) {
      "offer" -> transfers.onOffer(m.getString("id"), m.optString("kind", "file"), m.optString("name", "file"), m.optLong("size", -1))
      "clip" -> clip.onRemote(m.optString("data"), m.optLong("ts"))
      "cancel" -> transfers.onLaptopCancel(m.optString("id"))
      "token" -> m.optString("token").takeIf { it.isNotEmpty() }?.let { prefs.peerToken = it } // rotated by `sharingd rotate`
    }
  }

  // ---- outgoing ----

  fun sendClip(text: String) = clip.sendLocal(text)

  fun cancel(id: String) = transfers.cancel(id)
  fun retry(id: String) = transfers.retry(id)

  /**
   * Queues shared items. Unsafe URIs are refused, small files are copied to the
   * spool (the share grant dies with the process), and the work waits [UNDO_MS] so
   * the user can undo an accidental share from the notification.
   */
  fun uploadUris(uris: List<Uri>) {
    val laptop = prefs.laptopName ?: "your laptop"
    val dirs = listOfNotNull(ctx.filesDir.parent, ctx.dataDir?.path, ctx.cacheDir.parent, ctx.noBackupFilesDir.parent, ctx.getExternalFilesDir(null)?.parent)
    val authorities = setOf(ctx.packageName, "${ctx.packageName}.fileprovider", "${ctx.packageName}.provider")
    val queued = ArrayList<String>()
    val notBefore = System.currentTimeMillis() + UNDO_MS
    for (uri in uris) {
      val refusal = UriPolicy.reject(uri.toString(), authorities, dirs)
      if (refusal != null) {
        notifier.transfer("Not sent", refusal)
        continue
      }
      val meta = queryMeta(ctx.contentResolver, uri)
      val row =
        Transfer(direction = Dir.UP, name = Names.sanitize(meta.name), kind = "file", source = uri.toString(), size = meta.size, nextRetryAt = notBefore)
      spool.threshold = prefs.spoolThreshold
      val spooled = spool.copyIfSmall(ctx.contentResolver, uri, row.id, meta.size)
      val saved = if (spooled) row.copy(extra = JSONObject().put("spooled", true)) else row
      transfers.enqueue(saved)
      queued.add(row.id)
    }
    if (queued.isNotEmpty()) {
      notifier.undoable(queued.size, laptop, queued)
      session.poke(false)
    }
  }

  fun uploadTree(tree: Uri) {
    val name = Names.sanitize(Storage.treeName(ctx, tree))
    val row = Transfer(direction = Dir.UP, name = name, kind = "dir", source = tree.toString(), nextRetryAt = System.currentTimeMillis() + UNDO_MS)
    transfers.enqueue(row)
    notifier.undoable(1, prefs.laptopName ?: "your laptop", listOf(row.id))
    session.poke(false)
  }

  // ---- revision 1 laptops ----

  /** Folder download from a laptop without manifests: one live tar, no resume. */
  private fun legacyDirDownload(t: Transfer): Outcome {
    val r = remote() ?: return Outcome(error = IOException("laptop not reachable"), unreachable = true)
    val opened = r.open("/transfer/${t.source}", 0, null)
    return opened.use {
      if (!it.outcome.ok) return@use it.outcome
      try {
        Storage.untar(ctx, prefs.laptopName ?: "laptop", t.name, it.stream(), maxBytes = LEGACY_MAX_BYTES)
        Outcome(status = 200, progress = true)
      } catch (e: IOException) {
        Outcome(error = e)
      } catch (e: IllegalArgumentException) {
        Outcome(status = 422)
      }
    }
  }

  companion object {
    /** How long an accidental share can be undone before the upload starts. */
    const val UNDO_MS = 5_000L

    /** Byte cap for a revision 1 tar download, whose total size is not known in advance. */
    private const val LEGACY_MAX_BYTES = 64L shl 30
  }
}
