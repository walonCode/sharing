package expo.modules.sharingservice

import java.io.File
import java.io.FileOutputStream
import java.io.InputStream
import java.io.OutputStream
import java.io.RandomAccessFile

/** A [Sink] on a plain directory: pending files get a ".part" suffix until published. */
class FileSink(private val root: File) : Sink {
  val discarded = ArrayList<String>()

  /** Pending handle -> final path once published (a MediaStore URI stays valid; a file path does not). */
  private val published = HashMap<String, File>()

  override fun begin(folder: String?, sub: List<String>, name: String): String {
    var dir = root
    folder?.let { dir = File(dir, it) }
    sub.forEach { dir = File(dir, it) }
    dir.mkdirs()
    var n = 0
    while (true) {
      val f = File(dir, if (n == 0) "$name.part" else "$name ($n).part")
      if (f.createNewFile()) return f.path
      n++
    }
  }

  override fun length(handle: String) = File(handle).length()
  override fun truncate(handle: String, length: Long) {
    RandomAccessFile(handle, "rw").use { it.setLength(length) }
  }
  override fun append(handle: String): OutputStream = FileOutputStream(handle, true)
  override fun open(handle: String): InputStream = File(handle).inputStream()

  override fun publish(handle: String): String {
    val f = File(handle)
    val base = f.name.removeSuffix(".part")
    var n = 0
    while (true) {
      val target = File(
        f.parentFile,
        if (n ==
          0
        ) {
          base
        } else {
          "${base.substringBeforeLast('.', base)} ($n)${if (base.contains('.')) "." + base.substringAfterLast('.') else ""}"
        },
      )
      if (!target.exists()) {
        f.renameTo(target)
        published[handle] = target
        return target.name
      }
      n++
    }
  }

  override fun discard(handle: String) {
    discarded.add(handle)
    File(handle).delete()
    published[handle]?.delete()
  }
  override fun folderTaken(folder: String) = File(root, folder).exists()
}

/** Uploads from files on disk. [trees] maps a row's source to the folder it stands for. */
class DiskSources(private val files: Map<String, File> = emptyMap(), private val trees: Map<String, File> = emptyMap()) : SourceFactory {
  /** Wraps every opened source, e.g. to make it fail after some bytes. */
  var wrap: (File, UploadSource) -> UploadSource = { _, s -> s }
  private var currentRoot: File? = null

  override fun file(t: Transfer): UploadSource = files.getValue(t.source).let { wrap(it, FileSource(it)) }

  override fun tree(t: Transfer): List<TreeFile> {
    val root = trees.getValue(t.source)
    currentRoot = root
    return root.walkTopDown().filter { it.isFile }.map { TreeFile(it.relativeTo(root).path.replace(File.separatorChar, '/'), null, it.length()) }
      .sortedBy { it.relPath }.toList()
  }

  override fun source(f: TreeFile): UploadSource = File(currentRoot ?: trees.values.first(), f.relPath).let { wrap(it, FileSource(it)) }
}
