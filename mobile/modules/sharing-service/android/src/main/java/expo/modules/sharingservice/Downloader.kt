package expo.modules.sharingservice

import org.json.JSONObject
import java.io.IOException
import java.security.MessageDigest

/**
 * Runs one attempt of a download from the laptop. The partial file is a pending
 * entry in the [Sink]; the row remembers its handle and offset, and every
 * attempt first checks the entry's real length against that offset, so a crash
 * between a write and a database update can neither duplicate nor skip bytes.
 */
class Downloader(
  private val remote: () -> Remote?,
  private val store: TransferStore,
  private val sink: () -> Sink,
  private val onProgress: (Transfer, Long) -> Unit = { _, _ -> },
  /** Revision 1 laptops have no folder manifest; they send a live tar of the folder instead. */
  private val legacyDir: (Transfer) -> Outcome = { Outcome(status = 501) },
) {
  fun attempt(t: Transfer): Result {
    val r = remote() ?: return Result(Outcome(error = IOException("laptop not reachable"), unreachable = true), t)
    return try {
      if (t.extra.has("published")) finishAck(r, t)
      else if (t.kind == "dir") dirAttempt(r, t) else fileAttempt(r, t)
    } catch (e: IOException) {
      Result(Outcome(error = e, unreachable = isUnreachable(e)), store.get(t.id) ?: t)
    }
  }

  private fun persist(t: Transfer): Transfer { store.put(t); return t }
  private fun extra(t: Transfer, f: JSONObject.() -> Unit) = JSONObject(t.extra.toString()).apply(f)

  // ---- one file ----

  private fun fileAttempt(r: Remote, t0: Transfer): Result {
    var t = t0
    val s = sink()
    val handle = t.remoteId ?: s.begin(null, emptyList(), t.name).also { t = persist(t.copy(remoteId = it, offset = 0)) }
    val res = fetchInto(r, t, s, handle, "/transfer/${t.source}", t.size, t.offset)
    if (res.outcome.error != null || !res.outcome.ok) return res.copy(transfer = res.transfer)
    t = res.transfer
    verify(r, t.source, null, s, handle)?.let { mismatch ->
      s.truncate(handle, 0)
      val again = t.extra.optBoolean("sumRetried")
      if (again) return Result(Outcome(status = 422), persist(t.copy(offset = 0)), mismatch)
      return Result(Outcome(status = 412, ranged = true), persist(t.copy(offset = 0, extra = extra(t) { put("sumRetried", true) })), mismatch)
    }
    val name = s.publish(handle)
    t = persist(t.copy(extra = extra(t) { put("published", name) }, offset = maxOf(t.size, t.offset)))
    return finishAck(r, t)
  }

  /** Tells the laptop the download is complete; repeated until it hears it or the offer is gone. */
  private fun finishAck(r: Remote, t: Transfer): Result {
    val o = r.ack(t.source, "ok")
    return if (o.ok || o.status == 404 || o.status == 410) Result(Outcome(status = 200, progress = true), t) else Result(o, t)
  }

  /**
   * Appends the response for [path] to [handle], resuming at [offset]. Returns an ok outcome
   * once the entry holds [size] bytes (or the stream ended, for unknown sizes).
   */
  private fun fetchInto(r: Remote, t0: Transfer, s: Sink, handle: String, path: String, size: Long, offsetIn: Long, base: Long = 0): Result {
    var t = t0
    var offset = offsetIn
    val have = s.length(handle)
    if (have != offset) { // the entry and the database disagree: trust the entry's bytes, never past the stored offset
      offset = minOf(have, offset)
      s.truncate(handle, offset)
    }
    val etag = t.extra.optString("etag").ifEmpty { null }
    r.open(path, offset, etag).use { opened ->
      val o = opened.outcome
      if (!o.ok) {
        // 412/416 on a ranged request restart this file from zero (the retry policy allows it once).
        if (o.ranged && (o.status == 412 || o.status == 416)) { s.truncate(handle, 0); t = persist(t.copy(offset = base, extra = extra(t) { remove("etag") })) }
        return Result(o, t)
      }
      val resp = opened.response!!
      if (offset > 0 && resp.code == 200) { s.truncate(handle, 0); offset = 0 } // the server sent everything again (If-Range missed)
      resp.header("ETag")?.let { tag -> if (tag != etag) t = persist(t.copy(extra = extra(t) { put("etag", tag) })) }
      var lastSave = System.nanoTime()
      var advanced = false
      try {
        s.append(handle).use { out ->
          val input = opened.stream()
          val buf = ByteArray(Storage.COPY_BUFFER)
          while (true) {
            val n = input.read(buf)
            if (n < 0) break
            out.write(buf, 0, n)
            offset += n
            advanced = true
            onProgress(t, base + offset)
            if (System.nanoTime() - lastSave > 1_000_000_000L) { lastSave = System.nanoTime(); out.flush(); t = persist(t.copy(offset = base + offset)) }
          }
        }
      } catch (e: IOException) {
        // Whatever reached the entry is kept; the next attempt resumes from its real length.
        val kept = runCatching { s.length(handle) }.getOrDefault(offset)
        t = persist(t.copy(offset = base + kept))
        return Result(Outcome(error = e, progress = advanced, ranged = offset > 0, unreachable = false), t)
      }
      t = persist(t.copy(offset = base + offset))
      if (size >= 0 && offset != size) {
        return Result(Outcome(error = IOException("download ended at $offset of $size bytes"), progress = advanced), t)
      }
      return Result(Outcome(status = 200, progress = advanced), t)
    }
  }

  /** Compares the finished entry with the laptop's SHA-256 when it has one. Returns a message on mismatch. */
  private fun verify(r: Remote, id: String, index: Int?, s: Sink, handle: String): String? {
    val (single, many) = r.sums(id)
    val want = if (index == null) single else many?.getOrNull(index)
    if (want == null) return null
    val md = MessageDigest.getInstance("SHA-256")
    s.open(handle).use { input ->
      val buf = ByteArray(Storage.COPY_BUFFER)
      while (true) { val n = input.read(buf); if (n < 0) break; md.update(buf, 0, n) }
    }
    val got = md.digest().joinToString("") { "%02x".format(it) }
    return if (got.equals(want, ignoreCase = true)) null else "Checksum mismatch after resuming"
  }

  // ---- a folder, file by file ----

  private fun dirAttempt(r: Remote, t0: Transfer): Result {
    var t = t0
    val s = sink()
    val (mo, m) = r.manifest(t.source)
    if (mo.status == 404) return Result(legacyDir(t), t)
    if (!mo.ok || m == null) return Result(mo, t)
    if (m.files.size > SafWalker.MAX_ENTRIES) return Result(Outcome(status = 413), t, "Folder has too many files")
    var folder = t.extra.optString("folder")
    if (folder.isEmpty()) {
      val base = Names.sanitize(m.name)
      folder = base
      var n = 1
      while (s.folderTaken(folder)) folder = "$base (${n++})"
      t = persist(t.copy(size = m.files.sumOf { it.size }, extra = extra(t) { put("folder", folder) }))
    }
    var advanced = false
    var index = t.extra.optInt("index", 0)
    var doneBytes = t.extra.optLong("doneBytes", 0)
    while (index < m.files.size) {
      val f = m.files[index]
      val parts = f.path.split('/').filter { it.isNotEmpty() }
      if (parts.isEmpty() || parts.any { it == ".." || it == "." || Names.hasControlChars(it) }) {
        return Result(Outcome(status = 422), t, "Unsafe path in folder: ${f.path}")
      }
      var handle = t.extra.optString("current")
      if (handle.isEmpty()) {
        handle = s.begin(folder, parts.dropLast(1).map { Names.sanitize(it) }, parts.last())
        store.addArtifact(t.id, handle)
        t = persist(t.copy(extra = extra(t) { put("current", handle); remove("etag") }))
      }
      val res = fetchInto(r, t, s, handle, "/transfer/${t.source}/files/$index", f.size, maxOf(t.offset - doneBytes, 0), base = doneBytes)
      t = res.transfer
      if (res.outcome.progress) advanced = true
      if (!res.outcome.ok || res.outcome.error != null) return res.copy(outcome = res.outcome.copy(progress = advanced))
      verify(r, t.source, index, s, handle)?.let { msg ->
        s.truncate(handle, 0)
        return if (t.extra.optBoolean("sumRetried")) Result(Outcome(status = 422), t, msg)
        else Result(Outcome(status = 412, ranged = true), persist(t.copy(offset = doneBytes, extra = extra(t) { put("sumRetried", true) })), msg)
      }
      s.publish(handle)
      index++
      doneBytes += f.size
      t = persist(t.copy(offset = doneBytes, extra = extra(t) { put("index", index); put("doneBytes", doneBytes); put("current", ""); remove("etag"); remove("sumRetried") }))
    }
    t = persist(t.copy(extra = extra(t) { put("published", folder) }))
    return finishAck(r, t).let { it.copy(outcome = it.outcome.copy(progress = advanced || it.outcome.progress)) }
  }

  /** Deletes what this transfer created: the pending entry and, for folders, the files published so far. */
  fun discard(t: Transfer) {
    val s = sink()
    t.remoteId?.let { if (!t.extra.has("published")) s.discard(it) }
    t.extra.optString("current").takeIf { it.isNotEmpty() }?.let { s.discard(it) }
    if (t.kind == "dir" && !t.extra.has("published") || t.state == State.CANCELLED || t.state == State.FAILED) {
      store.artifacts(t.id).forEach { s.discard(it) }
    }
    store.clearArtifacts(t.id)
  }
}
