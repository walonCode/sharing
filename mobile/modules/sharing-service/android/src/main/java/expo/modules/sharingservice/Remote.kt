package expo.modules.sharingservice

import okhttp3.Call
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okio.BufferedSink
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.io.InputStream
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/** Cancels a call when no bytes have moved for [timeoutMs]; a half-dead WiFi link would otherwise hang a transfer. */
class StallGuard(private val call: Call, private val timeoutMs: Long = STALL_MS) : AutoCloseable {
  @Volatile private var last = System.nanoTime()
  @Volatile var stalled = false
    private set
  private val task: ScheduledFuture<*> = timer.scheduleWithFixedDelay({
    if ((System.nanoTime() - last) / 1_000_000 > timeoutMs) { stalled = true; call.cancel() }
  }, 1, 1, TimeUnit.SECONDS)

  fun touch() { last = System.nanoTime() }
  override fun close() { task.cancel(false) }

  companion object {
    const val STALL_MS = 30_000L
    private val timer = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "sharing-stall").also { it.isDaemon = true } }
  }
}

/** Counts bytes read so a [StallGuard] sees progress. */
class GuardedInput(private val src: InputStream, private val guard: StallGuard) : InputStream() {
  override fun read(): Int = src.read().also { if (it >= 0) guard.touch() }
  override fun read(b: ByteArray, off: Int, len: Int): Int = src.read(b, off, len).also { if (it > 0) guard.touch() }
  override fun close() = src.close()
}

data class Created(val id: String, val dirId: String?)
data class PatchResult(val outcome: Outcome, val offset: Long, val name: String?)

/**
 * The laptop's HTTP API (resumable uploads, ranged downloads, acks). Every call
 * reports an [Outcome] instead of throwing, so the retry policy sees one shape.
 * Blocking; call from a worker thread.
 */
class Remote(
  private val client: OkHttpClient,
  private val base: String,
  private val stallMs: Long = StallGuard.STALL_MS,
  private val token: () -> String?,
) {
  /** Short control calls (create, HEAD, ack...) must finish within 30 s even though transfers have no read timeout. */
  private val quick: OkHttpClient = client.newBuilder().callTimeout(30, TimeUnit.SECONDS).build()

  private fun req(path: String) = Request.Builder().url("$base$path")
    .header("Authorization", "Bearer ${token().orEmpty()}")
    .header("X-Sharing-Proto", "2")

  private fun outcome(resp: Response, patch: Boolean = false, ranged: Boolean = false) =
    Outcome(status = resp.code, retryAfterMs = Retry.retryAfterMs(resp.header("Retry-After")), patch = patch, ranged = ranged)

  private fun failed(e: Throwable, patch: Boolean = false, ranged: Boolean = false) =
    Outcome(error = e, patch = patch, ranged = ranged, unreachable = isUnreachable(e))

  // ---- uploads ----

  fun createUpload(name: String, kind: String, size: Long?, dirId: String? = null, relPath: String? = null): Pair<Outcome, Created?> {
    val spec = JSONObject().put("name", name).put("kind", kind)
    if (size != null && size >= 0) spec.put("size", size)
    if (dirId != null) spec.put("dirId", dirId)
    if (relPath != null) spec.put("relPath", relPath)
    return try {
      quick.newCall(req("/uploads").post(spec.toString().toRequestBody(JSON)).build()).execute().use { r ->
        val o = outcome(r)
        if (!o.ok) return o to null
        val j = JSONObject(r.body!!.string())
        o to Created(j.getString("id"), j.optString("dirId").ifEmpty { null })
      }
    } catch (e: IOException) { failed(e) to null }
  }

  /** Bytes the laptop has durably stored for the upload (HEAD). */
  fun uploadOffset(id: String): Pair<Outcome, Long> = try {
    quick.newCall(req("/uploads/$id").head().build()).execute().use { r ->
      outcome(r) to (r.header("Upload-Offset")?.toLongOrNull() ?: 0L)
    }
  } catch (e: IOException) { failed(e) to 0L }

  /**
   * Sends [length] bytes (or until EOF when null) from [source], which must be positioned at [offset].
   * [complete] marks the end of an upload whose size was unknown.
   */
  fun patch(id: String, offset: Long, source: InputStream, length: Long?, complete: Boolean, onBytes: (Long) -> Unit): PatchResult {
    lateinit var guard: StallGuard
    val body = object : RequestBody() {
      override fun contentType() = OCTET
      override fun contentLength() = length ?: -1L
      override fun writeTo(sink: BufferedSink) {
        val buf = ByteArray(BUF)
        var sent = 0L
        while (true) {
          val want = if (length == null) buf.size else minOf(buf.size.toLong(), length - sent).toInt()
          if (want <= 0) break
          val n = source.read(buf, 0, want)
          if (n < 0) break
          sink.write(buf, 0, n)
          sent += n
          guard.touch()
          onBytes(offset + sent)
        }
        if (length != null && sent != length) throw IOException("source ended after $sent of $length bytes")
      }
    }
    val rb = req("/uploads/$id").header("Upload-Offset", offset.toString()).method("PATCH", body)
    if (complete) rb.header("Upload-Complete", "true")
    val call = client.newCall(rb.build())
    guard = StallGuard(call, stallMs)
    return try {
      guard.use {
        call.execute().use { r ->
          val o = outcome(r, patch = true)
          val newOffset = r.header("Upload-Offset")?.toLongOrNull()
          when (r.code) {
            201 -> PatchResult(o, newOffset ?: (offset + (length ?: 0)), runCatching { JSONObject(r.body!!.string()).optString("name") }.getOrNull())
            else -> PatchResult(o, newOffset ?: offset, null)
          }
        }
      }
    } catch (e: IOException) {
      PatchResult(failed(if (guard.stalled) IOException("stalled: no bytes for ${StallGuard.STALL_MS / 1000}s", e) else e, patch = true), offset, null)
    }
  }

  fun deleteUpload(id: String): Outcome = simple(req("/uploads/$id").delete().build())

  fun completeDir(dirId: String, count: Int): Pair<Outcome, String?> = try {
    val b = JSONObject().put("count", count).toString().toRequestBody(JSON)
    quick.newCall(req("/dirs/$dirId/complete").post(b).build()).execute().use { r ->
      val o = outcome(r)
      o to if (o.ok) runCatching { JSONObject(r.body!!.string()).optString("name") }.getOrNull() else null
    }
  } catch (e: IOException) { failed(e) to null }

  // ---- downloads ----

  /** An open response plus the watchdog that cancels it when the link stalls. Close both via [close]. */
  class Opened(val outcome: Outcome, val response: Response?, val guard: StallGuard?) : AutoCloseable {
    override fun close() { guard?.close(); response?.close() }
    /** Input that feeds the watchdog as bytes arrive. */
    fun stream(): InputStream = GuardedInput(response!!.body!!.byteStream(), guard!!)
  }

  /** Opens GET [path] from byte [offset] (with If-Range [etag] when resuming). */
  fun open(path: String, offset: Long, etag: String?): Opened {
    val rb = req(path)
    if (offset > 0) {
      rb.header("Range", "bytes=$offset-")
      if (etag != null) rb.header("If-Range", etag)
    }
    val call = client.newCall(rb.build())
    val guard = StallGuard(call, stallMs)
    return try {
      val r = call.execute()
      val o = outcome(r, ranged = offset > 0)
      if (o.ok) Opened(o, r, guard) else { r.close(); guard.close(); Opened(o, null, null) }
    } catch (e: IOException) {
      guard.close()
      Opened(failed(if (guard.stalled) IOException("stalled: no bytes for ${StallGuard.STALL_MS / 1000}s", e) else e, ranged = offset > 0), null, null)
    }
  }

  /** Revision 1 upload: one PUT, no resume. */
  fun legacyPut(name: String, kind: String, length: Long, write: (java.io.OutputStream) -> Unit): Outcome {
    val body = object : RequestBody() {
      override fun contentType() = OCTET
      override fun contentLength() = length
      override fun writeTo(sink: BufferedSink) = write(sink.outputStream())
    }
    val q = "name=${java.net.URLEncoder.encode(name, "UTF-8")}&kind=$kind"
    return simple(req("/upload?$q").put(body).build())
  }

  fun manifest(id: String): Pair<Outcome, RemoteManifest?> = try {
    quick.newCall(req("/transfer/$id/manifest").build()).execute().use { r ->
      val o = outcome(r)
      o to if (o.ok) RemoteManifest.parse(r.body!!.string()) else null
    }
  } catch (e: IOException) { failed(e) to null }

  /** Digests once the laptop has computed them: a hex string for a file, a list for a folder; null while pending. */
  fun sums(id: String): Pair<String?, List<String>?> = try {
    quick.newCall(req("/transfer/$id/sha256").build()).execute().use { r ->
      if (!r.isSuccessful) return null to null
      val j = JSONObject(r.body!!.string())
      if (!j.optBoolean("ready")) return null to null
      val files = j.optJSONArray("files")
      j.optString("sha256").ifEmpty { null } to files?.let { a -> (0 until a.length()).map { a.getString(it) } }
    }
  } catch (e: IOException) { null to null } catch (e: org.json.JSONException) { null to null }

  fun ack(id: String, status: String, error: String? = null): Outcome {
    val b = JSONObject().put("status", status).apply { if (error != null) put("error", error) }.toString().toRequestBody(JSON)
    return simple(req("/transfer/$id/ack").post(b).build())
  }

  fun cancelOffer(id: String): Outcome = simple(req("/transfer/$id").delete().build())

  private fun simple(r: Request): Outcome = try {
    quick.newCall(r).execute().use { outcome(it) }
  } catch (e: IOException) { failed(e) }

  companion object {
    private val JSON = "application/json".toMediaType()
    private val OCTET = "application/octet-stream".toMediaType()
    const val BUF = 256 * 1024
  }
}

class RemoteEntry(val path: String, val size: Long, val mtime: Long)

class RemoteManifest(val name: String, val dirs: List<String>, val files: List<RemoteEntry>) {
  companion object {
    fun parse(text: String): RemoteManifest {
      val j = JSONObject(text)
      val dirs = j.optJSONArray("dirs") ?: JSONArray()
      val files = j.optJSONArray("files") ?: JSONArray()
      return RemoteManifest(
        j.optString("name", "folder"),
        (0 until dirs.length()).map { dirs.getString(it) },
        (0 until files.length()).map { files.getJSONObject(it).let { f -> RemoteEntry(f.getString("path"), f.getLong("size"), f.optLong("mtime")) } },
      )
    }
  }
}
