package expo.modules.sharingservice

import okhttp3.OkHttpClient
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okio.Buffer
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.File
import java.security.MessageDigest

/** Download edge cases against a scripted laptop (the real daemon is covered by InteropTest). */
class DownloaderTest {
  private lateinit var server: MockWebServer
  private lateinit var dir: File
  private val requests = ArrayList<RecordedRequest>()
  private val content = ByteArray(10_000) { (it * 31).toByte() }
  private fun sha(b: ByteArray) = MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

  @Before fun setUp() {
    server = MockWebServer()
    dir = java.nio.file.Files.createTempDirectory("dl").toFile()
  }

  @After fun tearDown() { server.shutdown(); dir.deleteRecursively() }

  private fun remote() = Remote(OkHttpClient(), server.url("/").toString().trimEnd('/'), token = { "tok" })

  private fun serve(honourRange: Boolean = true, sum: String? = sha(content), ackStatus: Int = 204, onFile: (RecordedRequest) -> MockResponse? = { null }) {
    server.dispatcher = object : Dispatcher() {
      override fun dispatch(request: RecordedRequest): MockResponse {
        synchronized(requests) { requests.add(request) }
        val path = request.path!!
        return when {
          path.endsWith("/sha256") -> MockResponse().setBody(if (sum == null) """{"ready":false}""" else """{"ready":true,"sha256":"$sum"}""")
          path.endsWith("/ack") -> MockResponse().setResponseCode(ackStatus)
          path.startsWith("/transfer/") -> onFile(request) ?: file(request, honourRange)
          else -> MockResponse().setResponseCode(404)
        }
      }
    }
  }

  private fun file(request: RecordedRequest, honourRange: Boolean): MockResponse {
    val range = request.getHeader("Range")
    if (range != null && honourRange) {
      val start = range.removePrefix("bytes=").removeSuffix("-").toInt()
      return MockResponse().setResponseCode(206).setHeader("ETag", "\"e1\"").setHeader("Content-Range", "bytes $start-${content.size - 1}/${content.size}")
        .setBody(Buffer().write(content, start, content.size - start))
    }
    return MockResponse().setHeader("ETag", "\"e1\"").setBody(Buffer().write(content))
  }

  private fun row(offset: Long = 0, handle: String? = null) =
    Transfer(direction = Dir.DOWN, name = "f.bin", kind = "file", source = "o1", size = content.size.toLong(), offset = offset, remoteId = handle)

  @Test fun resumesFromTheRealLengthOfThePartialFile() {
    serve()
    val sink = FileSink(dir)
    val handle = sink.begin(null, emptyList(), "f.bin")
    File(handle).writeBytes(content.copyOf(4000))
    val store = MemoryTransferStore()
    val t = row(offset = 4000, handle = handle).also { store.put(it) }
    val res = Downloader({ remote() }, store, { sink }).attempt(t)
    assertTrue("${res.outcome}", res.outcome.ok)
    assertEquals("bytes=4000-", requests.first { it.path == "/transfer/o1" }.getHeader("Range"))
    assertTrue(File(dir, "f.bin").readBytes().contentEquals(content))
  }

  @Test fun truncatesBytesWrittenPastTheStoredOffset() {
    serve()
    val sink = FileSink(dir)
    val handle = sink.begin(null, emptyList(), "f.bin")
    File(handle).writeBytes(content.copyOf(6000)) // the process died after writing 6000 but before saving that offset
    val store = MemoryTransferStore()
    val t = row(offset = 4000, handle = handle).also { store.put(it) }
    val res = Downloader({ remote() }, store, { sink }).attempt(t)
    assertTrue(res.outcome.ok)
    assertEquals("bytes=4000-", requests.first { it.path == "/transfer/o1" }.getHeader("Range"))
    assertTrue(File(dir, "f.bin").readBytes().contentEquals(content))
  }

  @Test fun restartsWhenTheServerIgnoresRange() {
    serve(honourRange = false)
    val sink = FileSink(dir)
    val handle = sink.begin(null, emptyList(), "f.bin")
    File(handle).writeBytes(content.copyOf(3000))
    val store = MemoryTransferStore()
    val t = row(offset = 3000, handle = handle).also { store.put(it) }
    val res = Downloader({ remote() }, store, { sink }).attempt(t)
    assertTrue("${res.outcome}", res.outcome.ok)
    assertTrue("no duplicated prefix", File(dir, "f.bin").readBytes().contentEquals(content))
  }

  @Test fun checksumMismatchRestartsOnceThenFailsWithAReason() {
    serve(sum = "0".repeat(64))
    val sink = FileSink(dir)
    val store = MemoryTransferStore()
    val d = Downloader({ remote() }, store, { sink })
    store.put(row())
    var res = d.attempt(row().also { store.put(it) })
    assertEquals(412, res.outcome.status)
    assertTrue(res.outcome.ranged)
    assertEquals(0L, res.transfer.offset)
    res = d.attempt(res.transfer)
    assertEquals(422, res.outcome.status)
    assertNotNull(res.message)
    assertTrue(!File(dir, "f.bin").exists())
  }

  @Test fun unfinishedLaptopHashDoesNotBlockTheDownload() {
    serve(sum = null)
    val store = MemoryTransferStore()
    val res = Downloader({ remote() }, store, { FileSink(dir) }).attempt(row().also { store.put(it) })
    assertTrue(res.outcome.ok)
    assertTrue(File(dir, "f.bin").readBytes().contentEquals(content))
  }

  @Test fun anAckLostInTransitIsRepeatedWithoutDownloadingAgain() {
    serve(ackStatus = 503)
    val sink = FileSink(dir)
    val store = MemoryTransferStore()
    val d = Downloader({ remote() }, store, { sink })
    var res = d.attempt(row().also { store.put(it) })
    assertEquals("the file is saved but the laptop did not hear about it", 503, res.outcome.status)
    assertTrue(File(dir, "f.bin").exists())
    val before = requests.count { it.path == "/transfer/o1" }
    serve(ackStatus = 204)
    res = d.attempt(res.transfer)
    assertTrue(res.outcome.ok)
    assertEquals("no second download", before, requests.count { it.path == "/transfer/o1" })
  }

  @Test fun anOfferTheLaptopForgotIsNotAFailureOfTheAck() {
    serve(ackStatus = 404)
    val store = MemoryTransferStore()
    assertTrue(Downloader({ remote() }, store, { FileSink(dir) }).attempt(row().also { store.put(it) }).outcome.ok)
  }

  @Test fun unsafeManifestPathsAreRefused() {
    server.dispatcher = object : Dispatcher() {
      override fun dispatch(request: RecordedRequest) = MockResponse().setBody(
        JSONObject().put("name", "evil").put("dirs", org.json.JSONArray()).put("files", org.json.JSONArray().put(JSONObject().put("path", "../../escape.txt").put("size", 1).put("mtime", 1))).toString(),
      )
    }
    val store = MemoryTransferStore()
    val t = Transfer(direction = Dir.DOWN, name = "evil", kind = "dir", source = "o2").also { store.put(it) }
    val res = Downloader({ remote() }, store, { FileSink(dir) }).attempt(t)
    assertEquals(422, res.outcome.status)
    assertTrue(res.message!!.contains("Unsafe"))
    assertTrue(dir.walkTopDown().none { it.isFile })
  }

  @Test fun anOldLaptopWithoutManifestsFallsBackToTheTarPath() {
    server.dispatcher = object : Dispatcher() { override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(404) }
    var used = false
    val store = MemoryTransferStore()
    val t = Transfer(direction = Dir.DOWN, name = "docs", kind = "dir", source = "o3").also { store.put(it) }
    val res = Downloader({ remote() }, store, { FileSink(dir) }, legacyDir = { used = true; Outcome(status = 200) }).attempt(t)
    assertTrue(used && res.outcome.ok)
  }

  @Test fun anUnreachableLaptopIsReportedAsSuch() {
    val res = Downloader({ null }, MemoryTransferStore(), { FileSink(dir) }).attempt(row())
    assertTrue(res.outcome.unreachable)
    assertEquals(Action.WAIT, Retry.classify(res.outcome))
  }
}
