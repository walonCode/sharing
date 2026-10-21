package expo.modules.sharingservice

import android.content.Context
import android.webkit.MimeTypeMap
import androidx.documentfile.provider.DocumentFile
import org.apache.commons.compress.archivers.tar.TarArchiveEntry
import org.apache.commons.compress.archivers.tar.TarArchiveInputStream
import org.apache.commons.compress.archivers.tar.TarArchiveOutputStream
import java.io.InputStream
import java.io.OutputStream

/** Reads and writes the user-chosen receive folder through the Storage Access Framework. */
object Storage {
  fun sanitize(name: String): String {
    val n = name.replace('\\', '/').substringAfterLast('/').trim()
    return if (n.isEmpty() || n == "." || n == "..") "file" else n
  }

  fun uniqueName(dir: DocumentFile, name: String): String {
    if (dir.findFile(name) == null) return name
    val dot = name.lastIndexOf('.').takeIf { it > 0 } ?: name.length
    val base = name.substring(0, dot)
    val ext = name.substring(dot)
    var i = 1
    while (dir.findFile("$base ($i)$ext") != null) i++
    return "$base ($i)$ext"
  }

  private fun mimeFor(name: String): String =
    MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase())
      ?: "application/octet-stream"

  fun root(ctx: Context, prefs: Prefs): DocumentFile {
    val uri = prefs.treeUri ?: error("No receive folder chosen")
    val dir = DocumentFile.fromTreeUri(ctx, uri)
    if (dir == null || !dir.canWrite()) error("Receive folder is no longer accessible; pick it again")
    return dir
  }

  fun saveFile(dir: DocumentFile, name: String, input: InputStream, ctx: Context): String {
    val finalName = uniqueName(dir, sanitize(name))
    val file = dir.createFile(mimeFor(finalName), finalName) ?: error("Could not create $finalName")
    try {
      ctx.contentResolver.openOutputStream(file.uri)!!.use { input.copyTo(it) }
    } catch (e: Exception) {
      file.delete() // never leave a half-written file behind
      throw e
    }
    return file.name ?: finalName
  }

  /** Extracts a tar whose first path component is the folder name (as sharingd sends it). */
  fun untar(dir: DocumentFile, rootName: String, input: InputStream, ctx: Context): String {
    val rootDirName = uniqueName(dir, sanitize(rootName))
    val root = dir.createDirectory(rootDirName) ?: error("Could not create $rootDirName")
    val dirs = HashMap<String, DocumentFile>().apply { put("", root) }

    fun dirFor(parts: List<String>): DocumentFile {
      var key = ""
      var cur = root
      for (p in parts) {
        key = if (key.isEmpty()) p else "$key/$p"
        cur = dirs.getOrPut(key) { cur.findFile(p) ?: cur.createDirectory(p) ?: error("Could not create $p") }
      }
      return cur
    }

    try {
      TarArchiveInputStream(input).use { tar ->
        while (true) {
          val e = tar.nextEntry ?: break
          val parts = e.name.replace('\\', '/').split('/').filter { it.isNotEmpty() }.drop(1)
          if (parts.isEmpty()) continue
          require(parts.none { it == ".." || it == "." }) { "Unsafe path in archive" }
          when {
            e.isDirectory -> dirFor(parts)
            e.isFile -> {
              val parent = dirFor(parts.dropLast(1))
              val name = uniqueName(parent, parts.last())
              val file = parent.createFile(mimeFor(name), name) ?: error("Could not create $name")
              ctx.contentResolver.openOutputStream(file.uri)!!.use { tar.copyTo(it) }
            }
          }
        }
      }
    } catch (ex: Exception) {
      root.delete()
      throw ex
    }
    return rootDirName
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
