package expo.modules.sharingservice

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Environment
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import androidx.documentfile.provider.DocumentFile
import org.apache.commons.compress.archivers.tar.TarArchiveEntry
import org.apache.commons.compress.archivers.tar.TarArchiveInputStream
import org.apache.commons.compress.archivers.tar.TarArchiveOutputStream
import java.io.InputStream
import java.io.OutputStream

/** Receives into Downloads/<laptop name> via MediaStore; reads a user-picked folder (SAF) when sending one. */
object Storage {
  fun sanitize(name: String): String {
    val n = name.replace('\\', '/').substringAfterLast('/').trim()
    return if (n.isEmpty() || n == "." || n == "..") "file" else n
  }

  private fun mimeFor(name: String): String =
    MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase())
      ?: "application/octet-stream"

  private val downloads = MediaStore.Downloads.EXTERNAL_CONTENT_URI

  /** "Download/<laptop>/<sub>/" — MediaStore creates the folders on demand and needs no permission for our own files. */
  private fun relativePath(laptop: String, vararg sub: String): String =
    (listOf(Environment.DIRECTORY_DOWNLOADS, sanitize(laptop)) + sub.toList()).joinToString("/") + "/"

  /** Creates a pending MediaStore entry; MediaStore itself renames on collision ("name (1).ext"). */
  private fun insert(ctx: Context, relPath: String, name: String): Uri {
    val values = ContentValues().apply {
      put(MediaStore.MediaColumns.DISPLAY_NAME, name)
      put(MediaStore.MediaColumns.MIME_TYPE, mimeFor(name))
      put(MediaStore.MediaColumns.RELATIVE_PATH, relPath)
      put(MediaStore.MediaColumns.IS_PENDING, 1)
    }
    return ctx.contentResolver.insert(downloads, values) ?: error("Could not create $name in Downloads")
  }

  private fun publish(ctx: Context, uri: Uri) {
    ctx.contentResolver.update(uri, ContentValues().apply { put(MediaStore.MediaColumns.IS_PENDING, 0) }, null, null)
  }

  private fun displayName(ctx: Context, uri: Uri, fallback: String): String =
    ctx.contentResolver.query(uri, arrayOf(MediaStore.MediaColumns.DISPLAY_NAME), null, null, null)?.use {
      if (it.moveToFirst()) it.getString(0) else null
    } ?: fallback

  /** Saves a file received from the laptop to Downloads/<laptop name>/. Returns the final file name. */
  fun saveFile(ctx: Context, laptop: String, name: String, input: InputStream): String {
    val clean = sanitize(name)
    val uri = insert(ctx, relativePath(laptop), clean)
    try {
      ctx.contentResolver.openOutputStream(uri)!!.use { input.copyTo(it) }
      publish(ctx, uri)
    } catch (e: Exception) {
      ctx.contentResolver.delete(uri, null, null) // never leave a half-written file behind
      throw e
    }
    return displayName(ctx, uri, clean)
  }

  private fun folderExists(ctx: Context, relPath: String): Boolean =
    ctx.contentResolver.query(
      downloads, arrayOf(MediaStore.MediaColumns._ID),
      "${MediaStore.MediaColumns.RELATIVE_PATH} LIKE ?", arrayOf("$relPath%"), null,
    )?.use { it.moveToFirst() } ?: false

  /**
   * Extracts a tar whose first path component is the folder name (as sharingd sends it)
   * into Downloads/<laptop name>/<folder>/. Returns the folder name used. Empty folders
   * are not recreated (MediaStore only knows files).
   */
  fun untar(ctx: Context, laptop: String, rootName: String, input: InputStream): String {
    val base = sanitize(rootName)
    var folder = base
    var n = 1
    while (folderExists(ctx, relativePath(laptop, folder))) folder = "$base (${n++})"

    val created = ArrayList<Uri>()
    try {
      TarArchiveInputStream(input).use { tar ->
        while (true) {
          val e = tar.nextEntry ?: break
          val parts = e.name.replace('\\', '/').split('/').filter { it.isNotEmpty() }.drop(1)
          if (parts.isEmpty() || !e.isFile) continue
          require(parts.none { it == ".." || it == "." }) { "Unsafe path in archive" }
          val uri = insert(ctx, relativePath(laptop, folder, *parts.dropLast(1).toTypedArray()), parts.last())
          created.add(uri)
          ctx.contentResolver.openOutputStream(uri)!!.use { tar.copyTo(it) }
        }
      }
      created.forEach { publish(ctx, it) }
    } catch (ex: Exception) {
      created.forEach { ctx.contentResolver.delete(it, null, null) }
      throw ex
    }
    return folder
  }

  /** Streams [folder] as a tar rooted at its own name. */
  fun tarTree(folder: DocumentFile, ctx: Context, out: OutputStream) {
    val tar = TarArchiveOutputStream(out)
    tar.setLongFileMode(TarArchiveOutputStream.LONGFILE_POSIX)
    tar.setBigNumberMode(TarArchiveOutputStream.BIGNUMBER_POSIX)

    fun add(f: DocumentFile, path: String) {
      if (f.isDirectory) {
        val e = TarArchiveEntry("$path/")
        tar.putArchiveEntry(e)
        tar.closeArchiveEntry()
        f.listFiles().forEach { add(it, "$path/${sanitize(it.name ?: "unnamed")}") }
      } else if (f.isFile) {
        val e = TarArchiveEntry(path)
        e.size = f.length()
        tar.putArchiveEntry(e)
        ctx.contentResolver.openInputStream(f.uri)?.use { it.copyTo(tar) }
        tar.closeArchiveEntry()
      }
    }
    add(folder, sanitize(folder.name ?: "folder"))
    tar.finish()
  }
}
