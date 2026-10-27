package expo.modules.sharingservice

import android.content.ContentResolver
import android.content.Context
import android.net.Uri
import java.io.File

/**
 * Share-sheet read grants usually die with the process, so a queued upload could
 * fail after a restart. Files below [threshold] are copied here when queued and
 * deleted on completion; bigger ones are only retried while the grant is alive.
 * The directory is in noBackupFilesDir.
 */
class Spool(ctx: Context, var threshold: Long = DEFAULT_THRESHOLD) {
  private val dir = File(ctx.applicationContext.noBackupFilesDir, "spool").also { it.mkdirs() }

  fun fileFor(id: String): File? = File(dir, id).takeIf { it.isFile }

  /** Copies [uri] if it is small enough; returns whether the row can survive losing its grant. */
  fun copyIfSmall(resolver: ContentResolver, uri: Uri, id: String, size: Long): Boolean {
    if (size < 0 || size > threshold) return false
    val target = File(dir, id)
    return try {
      val free = dir.usableSpace
      if (free < size + MIN_FREE) return false
      resolver.openInputStream(uri)?.use { input -> target.outputStream().use { input.copyTo(it, Storage.COPY_BUFFER) } } ?: return false
      target.length() == size
    } catch (e: Exception) {
      target.delete()
      false
    }
  }

  fun delete(id: String) { File(dir, id).delete() }

  /** Removes spooled files whose queue row is gone. */
  fun sweep(liveIds: Set<String>) {
    dir.listFiles()?.forEach { if (it.name !in liveIds) it.delete() }
  }

  companion object {
    const val DEFAULT_THRESHOLD = 256L * 1024 * 1024
    private const val MIN_FREE = 64L * 1024 * 1024
  }
}
