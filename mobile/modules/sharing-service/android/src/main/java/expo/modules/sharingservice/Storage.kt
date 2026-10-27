package expo.modules.sharingservice

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Environment
import android.provider.DocumentsContract
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import org.apache.commons.compress.archivers.tar.TarArchiveInputStream
import java.io.FileOutputStream
import java.io.InputStream
import java.io.OutputStream

/** Where finished and half-finished downloads go. MediaStore on the phone, a plain directory in tests. */
interface Sink {
  /** Creates an unpublished entry for [name] under [folder]/[sub] and returns its handle. */
  fun begin(folder: String?, sub: List<String>, name: String): String
  fun length(handle: String): Long
  fun truncate(handle: String, length: Long)
  /** Opens the entry for appending at its current end. */
  fun append(handle: String): OutputStream
  fun open(handle: String): InputStream
  /** Makes the entry visible and returns the name it ended up with. */
  fun publish(handle: String): String
  fun discard(handle: String)
  /** True if a folder of that name already holds files from an earlier transfer. */
  fun folderTaken(folder: String): Boolean
}

/** Receives into Downloads/<laptop name> via MediaStore; reads a user-picked folder (SAF) when sending one. */
object Storage {
  fun sanitize(name: String) = Names.sanitize(name)

  fun hasControlChars(s: String) = Names.hasControlChars(s)

  fun mimeFor(name: String): String =
    MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase())
      ?: "application/octet-stream"

  private val downloads = MediaStore.Downloads.EXTERNAL_CONTENT_URI

  /** "Download/<laptop>/<sub>/" - MediaStore creates the folders on demand and needs no permission for our own files. */
  fun relativePath(laptop: String, vararg sub: String): String =
    (listOf(Environment.DIRECTORY_DOWNLOADS, sanitize(laptop)) + sub.toList()).joinToString("/") + "/"

  /** Pending entries that are never published delete themselves after this long (seconds). */
  private const val PENDING_TTL_SECONDS = 3 * 24 * 3600L

  /** Creates a pending MediaStore entry; MediaStore itself renames on collision ("name (1).ext"). */
  fun insert(ctx: Context, relPath: String, name: String): Uri {
    val values = ContentValues().apply {
      put(MediaStore.MediaColumns.DISPLAY_NAME, name)
      put(MediaStore.MediaColumns.MIME_TYPE, mimeFor(name))
      put(MediaStore.MediaColumns.RELATIVE_PATH, relPath)
      put(MediaStore.MediaColumns.IS_PENDING, 1)
      put(MediaStore.MediaColumns.DATE_EXPIRES, System.currentTimeMillis() / 1000 + PENDING_TTL_SECONDS)
    }
    return ctx.contentResolver.insert(downloads, values) ?: error("Could not create $name in Downloads")
  }

  fun publish(ctx: Context, uri: Uri) {
    ctx.contentResolver.update(uri, ContentValues().apply {
      put(MediaStore.MediaColumns.IS_PENDING, 0)
      putNull(MediaStore.MediaColumns.DATE_EXPIRES)
    }, null, null)
  }

  fun displayName(ctx: Context, uri: Uri, fallback: String): String =
    ctx.contentResolver.query(uri, arrayOf(MediaStore.MediaColumns.DISPLAY_NAME), null, null, null)?.use {
      if (it.moveToFirst()) it.getString(0) else null
    } ?: fallback

  fun folderExists(ctx: Context, relPath: String): Boolean =
    ctx.contentResolver.query(
      downloads, arrayOf(MediaStore.MediaColumns._ID),
      "${MediaStore.MediaColumns.RELATIVE_PATH} LIKE ?", arrayOf("$relPath%"), null,
    )?.use { it.moveToFirst() } ?: false

  /** Saves a file received over the revision 1 protocol. Returns the final file name. */
  fun saveFile(ctx: Context, laptop: String, name: String, input: InputStream): String {
    val clean = sanitize(name)
    val uri = insert(ctx, relativePath(laptop), clean)
    try {
      ctx.contentResolver.openOutputStream(uri)!!.use { input.copyTo(it, COPY_BUFFER) }
      publish(ctx, uri)
    } catch (e: Exception) {
      ctx.contentResolver.delete(uri, null, null) // never leave a half-written file behind
      throw e
    }
    return displayName(ctx, uri, clean)
  }

  /**
   * Extracts a revision 1 tar whose first path component is the folder name into
   * Downloads/<laptop name>/<folder>/, refusing more than [maxEntries] entries or
   * [maxBytes] bytes and any name with control characters. Returns the folder name.
   */
  fun untar(
    ctx: Context, laptop: String, rootName: String, input: InputStream,
    maxEntries: Int = SafWalker.MAX_ENTRIES, maxBytes: Long = Long.MAX_VALUE,
  ): String {
    val base = sanitize(rootName)
    var folder = base
    var n = 1
    while (folderExists(ctx, relativePath(laptop, folder))) folder = "$base (${n++})"

    val created = ArrayList<Uri>()
    var total = 0L
    try {
      TarArchiveInputStream(input).use { tar ->
        while (true) {
          val e = tar.nextEntry ?: break
          require(created.size < maxEntries) { "Archive has too many entries" }
          require(!hasControlChars(e.name)) { "Unsafe name in archive" }
          val parts = e.name.replace('\\', '/').split('/').filter { it.isNotEmpty() }.drop(1)
          if (parts.isEmpty() || !e.isFile) continue
          require(parts.none { it == ".." || it == "." }) { "Unsafe path in archive" }
          val uri = insert(ctx, relativePath(laptop, folder, *parts.dropLast(1).toTypedArray()), parts.last())
          created.add(uri)
          ctx.contentResolver.openOutputStream(uri)!!.use { out ->
            val buf = ByteArray(COPY_BUFFER)
            while (true) {
              val r = tar.read(buf)
              if (r < 0) break
              total += r
              require(total <= maxBytes) { "Archive is too large" }
              out.write(buf, 0, r)
            }
          }
        }
      }
      created.forEach { publish(ctx, it) }
    } catch (ex: Exception) {
      created.forEach { ctx.contentResolver.delete(it, null, null) }
      throw ex
    }
    return folder
  }

  /** Display name of a picked folder. */
  fun treeName(ctx: Context, tree: Uri): String {
    val doc = DocumentsContract.buildDocumentUriUsingTree(tree, DocumentsContract.getTreeDocumentId(tree))
    return ctx.contentResolver.query(doc, arrayOf(DocumentsContract.Document.COLUMN_DISPLAY_NAME), null, null, null)?.use {
      if (it.moveToFirst()) it.getString(0) else null
    } ?: "folder"
  }

  /** Copy buffer for every stream loop; 8 KiB (the Kotlin default) is far below what a WiFi link can fill. */
  const val COPY_BUFFER = 256 * 1024
}

/** [Sink] backed by MediaStore Downloads/<laptop>/... */
class MediaStoreSink(private val ctx: Context, private val laptop: String) : Sink {
  override fun begin(folder: String?, sub: List<String>, name: String): String {
    val parts = listOfNotNull(folder) + sub
    return Storage.insert(ctx, Storage.relativePath(laptop, *parts.toTypedArray()), Storage.sanitize(name)).toString()
  }

  override fun length(handle: String): Long =
    ctx.contentResolver.openFileDescriptor(Uri.parse(handle), "r")?.use { it.statSize } ?: 0L

  override fun truncate(handle: String, length: Long) {
    ctx.contentResolver.openFileDescriptor(Uri.parse(handle), "rw")?.use { pfd ->
      FileOutputStream(pfd.fileDescriptor).use { it.channel.truncate(length) }
    }
  }

  override fun append(handle: String): OutputStream =
    ctx.contentResolver.openOutputStream(Uri.parse(handle), "wa") ?: error("Cannot write $handle")

  override fun open(handle: String): InputStream =
    ctx.contentResolver.openInputStream(Uri.parse(handle)) ?: error("Cannot read $handle")

  override fun publish(handle: String): String {
    val uri = Uri.parse(handle)
    Storage.publish(ctx, uri)
    return Storage.displayName(ctx, uri, "file")
  }

  override fun discard(handle: String) { runCatching { ctx.contentResolver.delete(Uri.parse(handle), null, null) } }

  override fun folderTaken(folder: String) = Storage.folderExists(ctx, Storage.relativePath(laptop, folder))
}
