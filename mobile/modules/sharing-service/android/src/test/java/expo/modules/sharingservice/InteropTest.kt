package expo.modules.sharingservice

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.net.ServerSocket
import java.net.URI
import java.net.URLDecoder
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * Drives the real laptop daemon with the real phone-side classes: resume in both
 * directions, folders, ack, and the stall guard. Runs only when SHARINGD_BIN points
 * at a built `sharingd` (CI builds it first); otherwise it is skipped.
 */
class InteropTest {
  private lateinit var work: File
  private lateinit var env: Map<String, String>
  private lateinit var bin: String
  private lateinit var daemon: Process
  private lateinit var remote: Remote
  private lateinit var sock: File

  private fun sh(vararg cmd: String): String {
    val pb = ProcessBuilder(*cmd).redirectErrorStream(true)
    pb.environment().putAll(env)
    val p = pb.start()
    val out = p.inputStream.bufferedReader().readText()
    p.waitFor(60, TimeUnit.SECONDS)
    return out
  }

  private fun control(path: String): String {
    val p = ProcessBuilder("curl", "-s", "--unix-socket", sock.path, "http://sharingd$path").redirectErrorStream(true).start()
    return p.inputStream.bufferedReader().readText().also { p.waitFor(30, TimeUnit.SECONDS) }
  }

  private fun sha(f: File): String {
    val md = MessageDigest.getInstance("SHA-256")
    f.inputStream().use { i ->
      val b = ByteArray(1 shl 16)
      while (true) {
        val n = i.read(b)
        if (n < 0) break
        md.update(b, 0, n)
      }
    }
    return md.digest().joinToString("") { "%02x".format(it) }
  }

  private fun randomFile(f: File, mib: Int, seed: Long): File {
    f.outputStream().use { o ->
      val r = java.util.Random(seed)
      val b = ByteArray(1 shl 20)
      repeat(mib) {
        r.nextBytes(b)
        o.write(b)
      }
    }
    return f
  }

  @Before fun startDaemon() {
    bin = System.getenv("SHARINGD_BIN") ?: ""
    assumeTrue("SHARINGD_BIN not set; skipping interop tests", bin.isNotEmpty() && File(bin).canExecute())
    work = java.nio.file.Files.createTempDirectory("interop").toFile()
    val cfg = File(work, "cfg").apply { mkdirs() }
    val run = File(work, "run").apply { mkdirs() }
    java.nio.file.Files.setPosixFilePermissions(run.toPath(), java.nio.file.attribute.PosixFilePermissions.fromString("rwx------"))
    sock = File(run, "sharing/sharingd.sock")
    val port = ServerSocket(0).use { it.localPort }
    File(cfg, "settings.json").writeText(
      """{"deviceId":"deadbeefdeadbeef","name":"testlaptop","receiveDir":"${File(
        work,
        "recv",
      ).path}","port":$port,"maxUploadBytes":68719476736,"maxEntries":200000,"maxTransfersPerPeer":4,"minFreeBytes":0,"maxPartialBytesPerPeer":274877906944,"confirmPairing":false}""",
    )
    env = mapOf("SHARING_CONFIG_DIR" to cfg.path, "XDG_RUNTIME_DIR" to run.path, "HOME" to work.path)
    daemon = ProcessBuilder(bin, "run").redirectErrorStream(true).redirectOutput(File(work, "daemon.log")).also { it.environment().putAll(env) }.start()
    val deadline = System.currentTimeMillis() + 15_000
    while (!sock.exists() && System.currentTimeMillis() < deadline) Thread.sleep(100)
    val link = sh(bin, "pair", "--yes").lines().first { it.startsWith("sharing://") }
    val q = URI(link).rawQuery.split("&").associate { it.substringBefore("=") to URLDecoder.decode(it.substringAfter("="), "UTF-8") }
    val http = Net.build(q.getValue("fp"))
    val body = JSONObject().put(
      "token",
      q.getValue("token"),
    ).put("deviceId", "phone-interop-01").put("name", "Interop Phone").toString().toRequestBody("application/json".toMediaType())
    val base = "https://127.0.0.1:$port"
    val token = http.newCall(Request.Builder().url("$base/pair").post(body).build()).execute().use { JSONObject(it.body!!.string()).getString("peerToken") }
    remote = Remote(http, base, stallMs = 2000, token = { token })
  }

  @After fun stopDaemon() {
    if (::daemon.isInitialized) {
      daemon.destroy()
      daemon.waitFor(5, TimeUnit.SECONDS)
    }
    if (::work.isInitialized) work.deleteRecursively()
  }

  /** A source that dies after [limit] bytes, like a provider or a link failing mid-transfer. */
  private class Dying(private val src: InputStream, private var limit: Long) : InputStream() {
    override fun read(): Int = if (limit-- <= 0) throw IOException("source died") else src.read()
    override fun read(b: ByteArray, off: Int, len: Int): Int {
      if (limit <= 0) throw IOException("source died")
      val n = src.read(b, off, minOf(len.toLong(), limit).toInt())
      if (n > 0) limit -= n
      return n
    }
    override fun close() = src.close()
  }

  private class DyingSource(private val inner: UploadSource, private val after: Long) : UploadSource {
    override fun size() = inner.size()
    override fun open(offset: Long): InputStream = Dying(inner.open(offset), after)
  }

  @Test fun uploadResumesAfterTheSourceDiesMidFile() {
    val big = randomFile(File(work, "big.bin"), 32, 7)
    val store = MemoryTransferStore()
    val sources = DiskSources(files = mapOf("big" to big))
    var die = true
    sources.wrap = { _, s -> if (die) DyingSource(s, 12_000_000) else s }
    val up = Uploader({ remote }, store, sources)
    var row = Transfer(direction = Dir.UP, name = "big.bin", kind = "file", source = "big", size = big.length())
    store.put(row)
    var res = up.attempt(row)
    assertNotNull(res.outcome.error)
    assertTrue("progress must be reported: ${res.transfer.offset}", res.outcome.progress && res.transfer.offset in 1 until big.length())
    die = false
    // The laptop may still be draining the dead connection, which shows up as a 409 (offset moved)
    // that the retry policy answers with an immediate retry; do the same here.
    row = res.transfer
    res = up.attempt(row)
    for (i in 1..3) {
      if (res.outcome.status != 409) break
      res = up.attempt(res.transfer)
    }
    assertEquals(201, res.outcome.status)
    val got = File(work, "recv/Interop Phone/big.bin")
    assertEquals(sha(big), sha(got))
    assertEquals(0, File(work, "recv/Interop Phone").listFiles { f -> f.name.startsWith(".partial-") }!!.size)
  }

  @Test fun uploadStartsOverWhenTheLaptopForgotTheSession() {
    val f = randomFile(File(work, "a.bin"), 2, 3)
    val store = MemoryTransferStore()
    val up = Uploader({ remote }, store, DiskSources(files = mapOf("a" to f)))
    val row = Transfer(direction = Dir.UP, name = "a.bin", kind = "file", source = "a", size = f.length(), remoteId = "no-such-session")
    store.put(row)
    var res = up.attempt(row)
    assertEquals("a vanished session is replaced (409/patch = retry now)", 409, res.outcome.status)
    assertNull(res.transfer.remoteId)
    res = up.attempt(res.transfer)
    assertEquals(201, res.outcome.status)
    assertEquals(sha(f), sha(File(work, "recv/Interop Phone/a.bin")))
  }

  @Test fun folderUploadIsInvisibleUntilCompleteThenResumes() {
    val tree = File(work, "album").apply { mkdirs() }
    repeat(5) { i -> File(tree, "sub$i").apply { mkdirs() }.let { File(it, "f$i.txt").writeText("content-$i".repeat(1000 * (i + 1))) } }
    val store = MemoryTransferStore()
    val sources = DiskSources(trees = mapOf("tree" to tree))
    var failOn = "sub2/f2.txt"
    sources.wrap = { file, s -> if (file.path.endsWith(failOn)) DyingSource(s, 100) else s }
    val up = Uploader({ remote }, store, sources)
    val row = Transfer(direction = Dir.UP, name = "album", kind = "dir", source = "tree")
    store.put(row)
    var res = up.attempt(row)
    assertNotNull(res.outcome.error)
    assertTrue(!File(work, "recv/Interop Phone/album").exists())
    failOn = "never"
    res = up.attempt(res.transfer)
    assertEquals(201, res.outcome.status)
    for (i in 0 until 5) assertEquals("content-$i".repeat(1000 * (i + 1)), File(work, "recv/Interop Phone/album/sub$i/f$i.txt").readText())
  }

  private fun offerId(name: String): String {
    val a = JSONArray(control("/transfers"))
    return (0 until a.length()).map { a.getJSONObject(it) }.first { it.getString("name") == name }.getString("id")
  }

  /** A sink whose append fails once after [dieAfter] bytes, like a connection dropping mid-download. */
  private class DyingSink(private val inner: Sink, var dieAfter: Long, private val only: String? = null) : Sink by inner {
    override fun append(handle: String): OutputStream {
      val out = inner.append(handle)
      if (only != null && !handle.contains(only)) return out
      val limit = dieAfter
      dieAfter = Long.MAX_VALUE
      var written = 0L
      return object : OutputStream() {
        override fun write(b: Int) = write(byteArrayOf(b.toByte()), 0, 1)
        override fun write(b: ByteArray, off: Int, len: Int) {
          val take = minOf(len.toLong(), limit - written).toInt()
          if (take > 0) {
            out.write(b, off, take)
            written += take
          }
          if (take < len) throw IOException("sink died")
        }
        override fun flush() = out.flush()
        override fun close() = out.close()
      }
    }
  }

  @Test fun downloadResumesWithRangeAndIsAcked() {
    val src = randomFile(File(work, "movie.bin"), 24, 9)
    sh(bin, "send", "--to", "Interop Phone", src.path)
    val id = offerId("movie.bin")
    val dir = File(work, "dl").apply { mkdirs() }
    val sink = DyingSink(FileSink(dir), 9_000_000)
    val store = MemoryTransferStore()
    val down = Downloader({ remote }, store, { sink })
    val row = Transfer(direction = Dir.DOWN, name = "movie.bin", kind = "file", source = id, size = src.length())
    store.put(row)
    var res = down.attempt(row)
    assertNotNull(res.outcome.error)
    assertEquals(9_000_000L, res.transfer.offset)
    res = down.attempt(res.transfer)
    assertTrue("status ${res.outcome.status} ${res.outcome.error}", res.outcome.ok)
    assertEquals(sha(src), sha(File(dir, "movie.bin")))
    val deadline = System.currentTimeMillis() + 3000
    var state = ""
    while (state != "done" && System.currentTimeMillis() < deadline) {
      state = JSONObject(control("/transfers/$id")).getString("state")
      Thread.sleep(50)
    }
    assertEquals("done", state)
  }

  @Test fun downloadRestartsWhenTheFileChangedOnTheLaptop() {
    val src = randomFile(File(work, "doc.bin"), 4, 11)
    sh(bin, "send", "--to", "Interop Phone", src.path)
    val id = offerId("doc.bin")
    val dir = File(work, "dl").apply { mkdirs() }
    val sink = DyingSink(FileSink(dir), 1_000_000)
    val store = MemoryTransferStore()
    val down = Downloader({ remote }, store, { sink })
    val row = Transfer(direction = Dir.DOWN, name = "doc.bin", kind = "file", source = id, size = src.length())
    store.put(row)
    val res = down.attempt(row)
    assertNotNull(res.outcome.error)
    src.appendBytes(byteArrayOf(1, 2, 3)) // changes size and mtime
    val again = down.attempt(res.transfer)
    assertEquals("the laptop answers 412 and the policy restarts from zero", 412, again.outcome.status)
    assertEquals(0L, again.transfer.offset)
  }

  @Test fun folderDownloadResumesFileByFile() {
    val src = File(work, "docs").apply { mkdirs() }
    repeat(4) { i -> File(src, "d$i").apply { mkdirs() }.let { File(it, "n$i.txt").writeText("doc-$i".repeat(5000)) } }
    sh(bin, "send", "--to", "Interop Phone", src.path)
    val id = offerId("docs")
    val dir = File(work, "dl2").apply { mkdirs() }
    val sink = DyingSink(FileSink(dir), 100, only = "/d2/")
    val store = MemoryTransferStore()
    val down = Downloader({ remote }, store, { sink })
    val row = Transfer(direction = Dir.DOWN, name = "docs", kind = "dir", source = id)
    store.put(row)
    var res = down.attempt(row)
    assertNotNull(res.outcome.error)
    assertTrue("earlier files stay published", File(dir, "docs/d0/n0.txt").exists() && File(dir, "docs/d1/n1.txt").exists())
    assertTrue("the half-written file is not published", !File(dir, "docs/d2/n2.txt").exists())
    res = down.attempt(res.transfer)
    assertTrue(res.outcome.ok)
    for (i in 0 until 4) assertEquals("doc-$i".repeat(5000), File(dir, "docs/d$i/n$i.txt").readText())
  }

  @Test fun failedFolderDownloadRemovesOnlyItsOwnFiles() {
    val src = File(work, "pics").apply { mkdirs() }
    repeat(3) { i -> File(src, "p$i.txt").writeText("pic-$i".repeat(100)) }
    sh(bin, "send", "--to", "Interop Phone", src.path)
    val id = offerId("pics")
    val dir = File(work, "dl3").apply { mkdirs() }
    File(dir, "unrelated.txt").writeText("keep me")
    val inner = FileSink(dir)
    val store = MemoryTransferStore()
    val down = Downloader({ remote }, store, { inner })
    var row = Transfer(direction = Dir.DOWN, name = "pics", kind = "dir", source = id)
    store.put(row)
    val res = down.attempt(row)
    assertTrue(res.outcome.ok)
    // Simulate a later failure of the whole transfer: discard removes what the transfer created, nothing else.
    store.put(res.transfer.copy(extra = JSONObject(res.transfer.extra.toString()).apply { remove("published") }, state = State.FAILED))
    down.discard(store.get(res.transfer.id)!!)
    assertTrue(File(dir, "unrelated.txt").exists())
    assertEquals(0, File(dir, "pics").walkTopDown().count { it.isFile })
  }

  @Test fun aSilentLaptopIsCutByTheStallGuard() {
    val srv = ServerSocket(0)
    Thread {
      runCatching {
        srv.accept().use { s ->
          s.getOutputStream().write("HTTP/1.1 200 OK\r\nContent-Length: 100000\r\n\r\nabc".toByteArray())
          Thread.sleep(20_000)
        }
      }
    }.also {
      it.isDaemon =
        true
    }.start()
    val plain = okhttp3.OkHttpClient.Builder().readTimeout(0, TimeUnit.SECONDS).build()
    val r = Remote(plain, "http://127.0.0.1:${srv.localPort}", stallMs = 1500, token = { "x" })
    val t0 = System.nanoTime()
    val opened = r.open("/transfer/x", 0, null)
    var err: Throwable? = null
    try {
      opened.stream().use { it.readBytes() }
    } catch (e: IOException) {
      err = e
    }
    opened.close()
    srv.close()
    assertNotNull(err)
    assertTrue("cut after ${(System.nanoTime() - t0) / 1e9}s", (System.nanoTime() - t0) / 1e9 < 8)
  }
}
