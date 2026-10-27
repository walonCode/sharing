package expo.modules.sharingservice

import android.content.ContentResolver
import android.content.Context
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.OpenableColumns
import java.io.File
import java.io.FileInputStream
import java.io.InputStream

/** Where an upload's bytes come from. [open] returns a stream positioned at [offset]. */
interface UploadSource {
  /** Total size in bytes, or -1 when the provider does not say. */
  fun size(): Long
  fun open(offset: Long): InputStream
}

/** Opens [skip]ped input: seeks when the descriptor allows it, otherwise reads and discards. */
fun InputStream.skipFully(n: Long): InputStream {
  var left = n
  while (left > 0) {
    val s = skip(left)
    if (s > 0) {
      left -= s
      continue
    }
    if (read() < 0) throw java.io.EOFException("source is shorter than the stored offset")
    left--
  }
  return this
}

class FileSource(private val file: File) : UploadSource {
  override fun size() = file.length()
  override fun open(offset: Long): InputStream = FileInputStream(file).also { it.channel.position(offset) }
}

class UriSource(private val resolver: ContentResolver, private val uri: Uri, private val knownSize: Long) : UploadSource {
  override fun size() = knownSize
  override fun open(offset: Long): InputStream {
    // A seekable descriptor lets a resume skip straight to the offset.
    runCatching {
      resolver.openFileDescriptor(uri, "r")?.let { pfd ->
        val input = ParcelFileDescriptorInput(pfd)
        if (offset == 0L || runCatching { input.channel.position(offset) }.isSuccess) return input
        input.close()
      }
    }.onFailure { if (it is SecurityException) throw it }
    return (resolver.openInputStream(uri) ?: throw java.io.FileNotFoundException("$uri")).skipFully(offset)
  }
}

private class ParcelFileDescriptorInput(private val pfd: android.os.ParcelFileDescriptor) : FileInputStream(pfd.fileDescriptor) {
  override fun close() {
    super.close()
    pfd.close()
  }
}

/** Display name and size of a content URI. */
data class UriMeta(val name: String, val size: Long)

fun queryMeta(resolver: ContentResolver, uri: Uri): UriMeta {
  var name = "shared-file"
  var size = -1L
  runCatching {
    resolver.query(uri, null, null, null, null)?.use { c ->
      if (c.moveToFirst()) {
        c.getColumnIndex(OpenableColumns.DISPLAY_NAME).takeIf { it >= 0 }?.let { name = c.getString(it) ?: name }
        c.getColumnIndex(OpenableColumns.SIZE).takeIf { it >= 0 && !c.isNull(it) }?.let { size = c.getLong(it) }
      }
    }
  }
  return UriMeta(name, size)
}

/** One file found under a picked folder. */
data class TreeFile(val relPath: String, val uri: Uri?, val size: Long)

/**
 * Lists a SAF tree with one child-document cursor per directory (id, name, mime,
 * size together) instead of a binder round trip per property per file.
 */
object SafWalker {
  private val cols = arrayOf(
    DocumentsContract.Document.COLUMN_DOCUMENT_ID,
    DocumentsContract.Document.COLUMN_DISPLAY_NAME,
    DocumentsContract.Document.COLUMN_MIME_TYPE,
    DocumentsContract.Document.COLUMN_SIZE,
  )

  fun walk(ctx: Context, tree: Uri, maxEntries: Int = MAX_ENTRIES): List<TreeFile> {
    val out = ArrayList<TreeFile>()
    fun visit(docId: String, prefix: String) {
      val children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, docId)
      val entries = ArrayList<Triple<String, String, Long>>() // id, name, size (-1 for dirs)
      ctx.contentResolver.query(children, cols, null, null, null)?.use { c ->
        while (c.moveToNext()) {
          val isDir = c.getString(2) == DocumentsContract.Document.MIME_TYPE_DIR
          entries.add(
            Triple(
              c.getString(0),
              Names.sanitize(c.getString(1) ?: "unnamed"),
              if (isDir) {
                -1L
              } else if (c.isNull(3)) {
                0L
              } else {
                c.getLong(3)
              },
            ),
          )
        }
      }
      for ((id, name, size) in entries.sortedBy { it.second }) {
        val rel = if (prefix.isEmpty()) name else "$prefix/$name"
        if (size < 0) {
          visit(id, rel)
        } else {
          if (out.size >= maxEntries) throw IllegalStateException("Folder has more than $maxEntries files")
          out.add(TreeFile(rel, DocumentsContract.buildDocumentUriUsingTree(tree, id), size))
        }
      }
    }
    visit(DocumentsContract.getTreeDocumentId(tree), "")
    return out.sortedBy { it.relPath }
  }

  const val MAX_ENTRIES = 200_000
}

/** Which content URIs the share target may forward to the laptop. Pure, so it is unit tested. */
object UriPolicy {
  /**
   * Accepts only `content://` URIs that do not point into this app. [ownAuthorities] are
   * the providers this app exposes (a FileProvider could serve its private files);
   * [ownDirs] are absolute paths of the app's private storage.
   */
  fun reject(uri: String, ownAuthorities: Set<String>, ownDirs: List<String>): String? {
    val scheme = uri.substringBefore(':', "").lowercase()
    if (scheme != "content") return "Only shared content can be sent (not $scheme: links)"
    val rest = uri.substringAfter("://", "")
    val authority = rest.substringBefore('/').substringAfterLast('@').lowercase()
    if (authority.isEmpty()) return "Shared item has no source"
    if (authority in ownAuthorities.map { it.lowercase() }) return "Refusing to send this app's own data"
    val decoded = runCatching { java.net.URLDecoder.decode(rest, "UTF-8") }.getOrDefault(rest)
    if (ownDirs.any { dir -> dir.isNotEmpty() && decoded.contains(dir) }) return "Refusing to send this app's own data"
    return null
  }
}
