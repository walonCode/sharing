package expo.modules.sharingservice

import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.SocketPolicy
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.ByteArrayInputStream

class RemoteTest {
  private lateinit var server: MockWebServer

  @Before fun setUp() { server = MockWebServer().also { it.start() } }
  @After fun tearDown() { server.shutdown() }

  private fun remote(stallMs: Long = StallGuard.STALL_MS) = Remote(OkHttpClient(), server.url("/").toString().trimEnd('/'), stallMs, token = { "tok" })

  @Test fun everyRequestCarriesTheTokenAndProtocolHeader() {
    server.enqueue(MockResponse().setResponseCode(201).setBody("""{"id":"u1"}"""))
    val (o, c) = remote().createUpload("a.bin", "file", 5)
    assertTrue(o.ok); assertEquals("u1", c!!.id)
    val req = server.takeRequest()
    assertEquals("Bearer tok", req.getHeader("Authorization"))
    assertEquals("2", req.getHeader("X-Sharing-Proto"))
    assertTrue(req.body.readUtf8().contains(""""size":5"""))
  }

  @Test fun patchWithAWrongOffsetIsClassifiedAsRetryNow() {
    server.enqueue(MockResponse().setResponseCode(409).setHeader("Upload-Offset", "42"))
    val r = remote().patch("u1", 10, ByteArrayInputStream(ByteArray(5)), 5, false) {}
    assertEquals(409, r.outcome.status)
    assertEquals(42L, r.offset)
    assertEquals(Action.NOW, Retry.classify(r.outcome))
  }

  @Test fun finalPatchReturnsTheFileName() {
    server.enqueue(MockResponse().setResponseCode(201).setBody("""{"name":"a (1).bin"}"""))
    val r = remote().patch("u1", 0, ByteArrayInputStream(ByteArray(5)), 5, false) {}
    assertEquals(201, r.outcome.status); assertEquals("a (1).bin", r.name)
    val req = server.takeRequest()
    assertEquals("PATCH", req.method); assertEquals("0", req.getHeader("Upload-Offset")); assertNull(req.getHeader("Upload-Complete"))
  }

  @Test fun unknownSizeUploadsMarkTheLastPatchComplete() {
    server.enqueue(MockResponse().setResponseCode(201).setBody("""{"name":"s"}"""))
    remote().patch("u1", 0, ByteArrayInputStream(ByteArray(5)), null, true) {}
    assertEquals("true", server.takeRequest().getHeader("Upload-Complete"))
  }

  @Test fun aSourceShorterThanPromisedFailsTheAttempt() {
    server.enqueue(MockResponse().setResponseCode(204))
    val r = remote().patch("u1", 0, ByteArrayInputStream(ByteArray(3)), 10, false) {}
    assertNotNull("a truncated body must not look like success", r.outcome.error)
  }

  @Test fun retryAfterIsParsed() {
    server.enqueue(MockResponse().setResponseCode(503).setHeader("Retry-After", "7"))
    val (o, _) = remote().uploadOffset("u1")
    assertEquals(7_000L, o.retryAfterMs)
    assertEquals(Action.BACKOFF, Retry.classify(o))
  }

  @Test fun aRangeThatTheFileOutgrewIsRestartable() {
    server.enqueue(MockResponse().setResponseCode(412))
    val opened = remote().open("/transfer/x", 100, "\"e\"")
    assertTrue(opened.outcome.ranged)
    assertEquals(Action.RESTART, Retry.classify(opened.outcome))
    val req = server.takeRequest()
    assertEquals("bytes=100-", req.getHeader("Range")); assertEquals("\"e\"", req.getHeader("If-Range"))
  }

  @Test fun aStalledPatchIsCutAndReportedAsAStall() {
    server.enqueue(MockResponse().setSocketPolicy(SocketPolicy.NO_RESPONSE))
    val t0 = System.nanoTime()
    val r = remote(stallMs = 1200).patch("u1", 0, ByteArrayInputStream(ByteArray(5)), 5, false) {}
    assertNotNull(r.outcome.error)
    assertTrue(r.outcome.error!!.message!!.contains("stalled"))
    assertTrue((System.nanoTime() - t0) / 1e9 < 8)
  }

  @Test fun noServerMeansUnreachableNotAFailedTransfer() {
    val dead = Remote(OkHttpClient(), "http://127.0.0.1:1", token = { "x" })
    val (o, _) = dead.uploadOffset("u1")
    assertTrue(o.unreachable)
    assertEquals(Action.WAIT, Retry.classify(o))
  }

  @Test fun manifestParsing() {
    val m = RemoteManifest.parse("""{"name":"docs","dirs":["a","b"],"files":[{"path":"a/x","size":3,"mtime":9}]}""")
    assertEquals("docs", m.name); assertEquals(listOf("a", "b"), m.dirs); assertEquals(3L, m.files[0].size)
  }
}
