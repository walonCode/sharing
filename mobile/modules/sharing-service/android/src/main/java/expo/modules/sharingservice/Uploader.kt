package expo.modules.sharingservice

import android.net.Uri
import org.json.JSONObject
import java.io.FileNotFoundException
import java.io.IOException

/** What an attempt achieved: the [outcome] for the retry policy, the updated row, and a reason to show on failure. */
data class Result(val outcome: Outcome, val transfer: Transfer, val message: String? = null)

/** Resolves a queue row to bytes. Android supplies content resolvers; tests supply files. */
interface SourceFactory {
  /** The bytes of a file row. May throw SecurityException or FileNotFoundException when access is gone. */
  fun file(t: Transfer): UploadSource
  /** Lists the files of a folder row, sorted by relative path. */
  fun tree(t: Transfer): List<TreeFile>
  fun source(f: TreeFile): UploadSource
}

/**
 * Runs one attempt of an upload against the laptop's resumable API. Everything
 * needed to continue after a crash lives in the [Transfer] row (upload id,
 * folder index); the laptop's stored length is the source of truth for the offset.
 */
class Uploader(
  private val remote: () -> Remote?,
  private val store: TransferStore,
  private val sources: SourceFactory,
  /** Called with (id, bytes sent so far) as data moves. */
  private val onProgress: (Transfer, Long) -> Unit = { _, _ -> },
  /** True once a laptop answered that it only speaks revision 1. */
  private val legacy: () -> Boolean = { false },
  private val setLegacy: () -> Unit = {},
) {
  fun attempt(t: Transfer): Result {
    val r = remote() ?: return Result(Outcome(error = IOException("laptop not reachable"), unreachable = true), t)
    return try {
      if (legacy()) legacyAttempt(r, t)
      else if (t.kind == "dir") dirAttempt(r, t) else fileAttempt(r, t)
    } catch (e: SecurityException) {
      Result(Outcome(status = 422), t, "Needs re-share: Android no longer lets this app read the file")
    } catch (e: FileNotFoundException) {
      Result(Outcome(status = 422), t, "The file is no longer available")
    }
  }

  private fun persist(t: Transfer): Transfer { store.put(t); return t }

  private fun fileAttempt(r: Remote, t0: Transfer): Result {
    var t = t0
    val src = sources.file(t)
    val size = src.size().takeIf { it >= 0 } ?: t.size
    if (t.remoteId == null) {
      val (o, c) = r.createUpload(t.name, "file", size.takeIf { it >= 0 })
      if (isLegacyAnswer(o)) { setLegacy(); return Result(Outcome(status = 409, patch = true), t) }
      if (!o.ok) return Result(o, t)
      t = persist(t.copy(remoteId = c!!.id, offset = 0, size = size))
    }
    val (ho, stored) = r.uploadOffset(t.remoteId!!)
    if (ho.status == 404) return Result(Outcome(status = 409, patch = true), persist(t.copy(remoteId = null, offset = 0))) // session expired: start over
    if (!ho.ok) return Result(ho, t)
    if (size >= 0 && stored > size) { // laptop has more than the source: the source changed; start over
      r.deleteUpload(t.remoteId!!)
      return Result(Outcome(status = 409, patch = true), persist(t.copy(remoteId = null, offset = 0)))
    }
    t = persist(t.copy(offset = stored))
    val input = src.open(stored)
    val remaining = if (size >= 0) size - stored else null
    val res = input.use {
      r.patch(t.remoteId!!, stored, it, remaining, complete = size < 0) { sent -> onProgress(t, sent) }
    }
    val landed = if (res.outcome.status == 201) res.offset else landedOffset(r, t.remoteId!!, res)
    val progressed = landed > stored
    val o = res.outcome.copy(progress = progressed)
    t = persist(t.copy(offset = landed))
    if (o.status == 201) return Result(o, t)
    // 204: the laptop expected more bytes than the source gave.
    if (o.ok) return Result(Outcome(status = 409, patch = true, progress = progressed), t)
    return Result(o, t)
  }

  /**
   * After a PATCH that did not finish cleanly, asks the laptop how much of it landed: the
   * connection may have died after the laptop stored part of the body, and that counts as progress.
   */
  private fun landedOffset(r: Remote, id: String, res: PatchResult): Long {
    if (res.outcome.error == null) return res.offset
    val (o, off) = r.uploadOffset(id)
    return if (o.ok) maxOf(off, res.offset) else res.offset
  }

  private fun dirAttempt(r: Remote, t0: Transfer): Result {
    var t = t0
    val files = sources.tree(t)
    val total = files.sumOf { it.size }
    t = t.copy(size = total)
    var dirId = t.extra.optString("dirId")
    if (dirId.isEmpty()) {
      val (o, c) = r.createUpload(t.name, "dir", null)
      if (isLegacyAnswer(o)) { setLegacy(); return Result(Outcome(status = 409, patch = true), t) }
      if (!o.ok) return Result(o, t)
      dirId = c!!.id
      t = persist(t.copy(extra = JSONObject(t.extra.toString()).put("dirId", dirId)))
    }
    var advanced = false
    var index = t.extra.optInt("index", 0)
    var doneBytes = t.extra.optLong("doneBytes", 0)
    while (index < files.size) {
      val f = files[index]
      var fileId = t.extra.optString("fileId")
      if (fileId.isEmpty()) {
        val (o, c) = r.createUpload(f.relPath.substringAfterLast('/'), "file-in-dir", f.size, dirId, f.relPath)
        if (!o.ok) {
          if (o.status == 404) { // the folder session expired: start the whole folder again
            return Result(Outcome(status = 409, patch = true), persist(t.copy(offset = 0, extra = JSONObject().put("restart", true))))
          }
          return Result(o.copy(progress = advanced), t)
        }
        fileId = c!!.id
        t = persist(t.copy(extra = JSONObject(t.extra.toString()).put("fileId", fileId)))
      }
      val (ho, stored) = r.uploadOffset(fileId)
      if (ho.status == 404) {
        t = persist(t.copy(extra = JSONObject(t.extra.toString()).put("fileId", "")))
        return Result(Outcome(status = 409, patch = true, progress = advanced), t)
      }
      if (!ho.ok) return Result(ho.copy(progress = advanced), t)
      val base = doneBytes
      val input = sources.source(f).open(stored)
      val res = input.use {
        r.patch(fileId, stored, it, f.size - stored, complete = false) { sent -> onProgress(t, base + sent) }
      }
      val landed = if (res.outcome.status == 201) res.offset else landedOffset(r, fileId, res)
      if (landed > stored) advanced = true
      if (res.outcome.status != 201) {
        t = persist(t.copy(offset = base + landed))
        return Result(res.outcome.copy(progress = advanced), t)
      }
      index++
      doneBytes += f.size
      t = persist(t.copy(offset = doneBytes, extra = JSONObject(t.extra.toString()).put("index", index).put("fileId", "").put("doneBytes", doneBytes)))
    }
    val (co, _) = r.completeDir(dirId, files.size)
    return Result(co.copy(progress = advanced), t)
  }

  /** Revision 1 laptops: one PUT, restarted from zero on failure. */
  private fun legacyAttempt(r: Remote, t: Transfer): Result {
    if (t.kind == "dir") {
      val files = sources.tree(t)
      val total = files.sumOf { it.size }
      val o = r.legacyPut(t.name, "dir", -1) { out -> tarOut(t, files, out) }
      return Result(o, persist(t.copy(size = total, offset = if (o.ok) total else 0)))
    }
    val src = sources.file(t)
    val size = src.size()
    val o = r.legacyPut(t.name, "file", size) { out -> src.open(0).use { it.copyTo(out, Storage.COPY_BUFFER) } }
    return Result(o, persist(t.copy(offset = if (o.ok) maxOf(size, 0) else 0)))
  }

  private fun tarOut(t: Transfer, files: List<TreeFile>, out: java.io.OutputStream) {
    val tar = org.apache.commons.compress.archivers.tar.TarArchiveOutputStream(out)
    tar.setLongFileMode(org.apache.commons.compress.archivers.tar.TarArchiveOutputStream.LONGFILE_POSIX)
    tar.setBigNumberMode(org.apache.commons.compress.archivers.tar.TarArchiveOutputStream.BIGNUMBER_POSIX)
    val root = Names.sanitize(t.name)
    for (f in files) {
      val e = org.apache.commons.compress.archivers.tar.TarArchiveEntry("$root/${f.relPath}")
      e.size = f.size
      tar.putArchiveEntry(e)
      sources.source(f).open(0).use { it.copyTo(tar, Storage.COPY_BUFFER) }
      tar.closeArchiveEntry()
    }
    tar.finish()
  }

  /** A laptop that predates resumable uploads has no /uploads route. */
  private fun isLegacyAnswer(o: Outcome) = o.status == 404 || o.status == 405 || o.status == 501
}

/** Builds the real sources on Android. */
class AndroidSources(private val ctx: android.content.Context, private val spool: Spool) : SourceFactory {
  override fun file(t: Transfer): UploadSource {
    spool.fileFor(t.id)?.let { return FileSource(it) }
    val uri = Uri.parse(t.source)
    return UriSource(ctx.contentResolver, uri, t.size)
  }

  override fun tree(t: Transfer): List<TreeFile> = SafWalker.walk(ctx, Uri.parse(t.source))

  override fun source(f: TreeFile): UploadSource = UriSource(ctx.contentResolver, f.uri ?: throw FileNotFoundException(f.relPath), f.size)
}
