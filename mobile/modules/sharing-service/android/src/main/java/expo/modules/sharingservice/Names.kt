package expo.modules.sharingservice

/** Name hygiene shared by uploads, downloads and the queue. No Android classes, so it is trivially testable. */
object Names {
  /** Reduces a name from the laptop to one safe path element without control characters. */
  fun sanitize(name: String): String {
    val n = name.replace('\\', '/').substringAfterLast('/').filter { it.code >= 0x20 && it.code != 0x7f }.trim()
    return if (n.isEmpty() || n == "." || n == "..") "file" else n
  }

  fun hasControlChars(s: String) = s.any { it.code < 0x20 || it.code == 0x7f }
}
