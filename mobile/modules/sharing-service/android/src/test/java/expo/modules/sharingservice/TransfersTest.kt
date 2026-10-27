package expo.modules.sharingservice

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

class TransfersTest {
  private val store = MemoryTransferStore()
  private val events = ArrayList<Pair<String, String>>()
  private var transfers: Transfers? = null

  @After fun tearDown() {
    transfers?.stop()
  }

  private fun up(name: String = "a.bin") = Transfer(direction = Dir.UP, name = name, kind = "file", source = "content://x/$name", size = 10)
  private fun ok() = Outcome(status = 201)

  private fun build(
    clock: () -> Long = System::currentTimeMillis,
    workers: Int = 2,
    discarded: MutableList<String> = ArrayList(),
    attemptUp: (Transfer) -> Result,
    attemptDown: (Transfer) -> Result = { Result(ok(), it) },
    policy: Retry.Policy = Retry.Policy(jitter = { 0 }),
  ): Transfers {
    val ev = object : TransferEvents {
      override fun changed(t: Transfer) {
        synchronized(events) { events.add(t.id to t.state) }
      }
    }
    return Transfers(store, attemptUp, attemptDown, { discarded.add(it.id) }, ev, policy, workers, clock, remote = { null }).also { transfers = it }
  }

  private fun waitFor(what: String, cond: () -> Boolean) {
    val end = System.currentTimeMillis() + 5_000
    while (System.currentTimeMillis() < end) {
      if (cond()) return
      Thread.sleep(10)
    }
    throw AssertionError("timed out waiting for $what; rows=${store.all().map { it.name to it.state }}")
  }

  @Test fun runsARowToCompletion() {
    val t = build(attemptUp = { Result(ok(), it) })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("done") { store.get(row.id)?.state == State.DONE }
    assertEquals(10, store.get(row.id)!!.offset)
  }

  @Test fun failedAttemptsBackOffAndResumeLater() {
    val now = 1_000_000L
    val calls = AtomicInteger()
    val t = build(clock = { now }, policy = Retry.Policy(jitter = { it }), attemptUp = {
      if (calls.incrementAndGet() == 1) Result(Outcome(error = IOException("wifi dropped")), it) else Result(ok(), it)
    })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("retry scheduled") { store.get(row.id)?.let { it.state == State.QUEUED && it.nextRetryAt > now } == true }
    assertEquals(1, calls.get())
    assertEquals("wifi dropped", store.get(row.id)!!.error)
    assertEquals(1, store.get(row.id)!!.attempts)
    // The wait is skipped when the network returns.
    t.retryNow()
    waitFor("done") { store.get(row.id)?.state == State.DONE }
    assertEquals(2, calls.get())
  }

  @Test fun offsetMismatchRetriesImmediately() {
    val calls = AtomicInteger()
    val t = build(attemptUp = { if (calls.incrementAndGet() < 3) Result(Outcome(status = 409, patch = true), it) else Result(ok(), it) })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("done") { store.get(row.id)?.state == State.DONE }
    assertEquals(3, calls.get())
  }

  @Test fun unreachableLaptopWaitsWithoutFailing() {
    val t = build(attemptUp = { Result(Outcome(error = IOException("no route"), unreachable = true), it) })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("waiting") { store.get(row.id)?.let { it.state == State.QUEUED && it.error == "Waiting for your laptop" } == true }
    assertEquals(0, store.get(row.id)!!.attempts)
  }

  @Test fun permanentFailuresStopWithAReason() {
    val t = build(attemptUp = { Result(Outcome(status = 507), it) })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("failed") { store.get(row.id)?.state == State.FAILED }
    assertEquals("The laptop's disk is full", store.get(row.id)!!.error)
  }

  @Test fun givesUpAfterEightAttemptsWithoutProgress() {
    val calls = AtomicInteger()
    var now = 1_000L
    val t =
      build(clock = { now }, policy = Retry.Policy(jitter = { it }, clock = { now }), attemptUp = {
        calls.incrementAndGet()
        Result(Outcome(status = 503), it)
      })
    t.start()
    val row = up()
    t.enqueue(row)
    for (i in 1..20) {
      waitFor("attempt $i") { calls.get() >= i || store.get(row.id)?.state == State.FAILED }
      if (store.get(row.id)?.state == State.FAILED) break
      waitFor("queued again") { store.get(row.id)?.state == State.QUEUED }
      now += 120_000
      t.retryNow()
    }
    waitFor("failed") { store.get(row.id)?.state == State.FAILED }
    assertEquals(9, calls.get()) // the ninth consecutive failure exceeds the budget of eight retries
  }

  @Test fun authFailureStopsAndSignalsPairAgain() {
    val authFailed = CountDownLatch(1)
    val ev = object : TransferEvents {
      override fun authFailed() = authFailed.countDown()
    }
    val t = Transfers(store, { Result(Outcome(status = 401), it) }, { Result(ok(), it) }, {}, ev, Retry.Policy(), 2)
    transfers = t
    t.start()
    val row = up()
    t.enqueue(row)
    assertTrue(authFailed.await(5, TimeUnit.SECONDS))
    waitFor("failed") { store.get(row.id)?.state == State.FAILED }
  }

  @Test fun goneOffersAreDropped() {
    val discarded = ArrayList<String>()
    val t = build(discarded = discarded, attemptUp = { Result(ok(), it) }, attemptDown = { Result(Outcome(status = 410), it) })
    t.start()
    t.onOffer("offer-1", "file", "movie.mp4", 100)
    waitFor("dropped") { store.all().isEmpty() }
  }

  @Test fun atMostTwoRowsRunAtOnce() {
    val running = AtomicInteger()
    val peak = AtomicInteger()
    val release = CountDownLatch(1)
    val t = build(workers = 2, attemptUp = {
      val n = running.incrementAndGet()
      peak.updateAndGet { p -> maxOf(p, n) }
      release.await(5, TimeUnit.SECONDS)
      running.decrementAndGet()
      Result(ok(), it)
    })
    t.start()
    repeat(50) { t.enqueue(up("f$it")) } // sharing 50 photos must not start 50 uploads
    waitFor("two running") { running.get() == 2 }
    Thread.sleep(150)
    assertEquals(2, running.get())
    release.countDown()
    waitFor("all done") { store.all().all { it.state == State.DONE } }
    assertEquals(2, peak.get())
  }

  @Test fun queueSurvivesAProcessRestart() {
    val gate = CountDownLatch(1)
    val first = build(attemptUp = {
      gate.await()
      Result(Outcome(error = IOException("killed")), it)
    })
    first.start()
    val a = up("a")
    val b = up("b")
    first.enqueue(a)
    first.enqueue(b)
    waitFor("both running") { store.all().count { it.state == State.RUNNING } == 2 }
    first.stop() // the process dies with rows still marked RUNNING

    val done = ConcurrentHashMap.newKeySet<String>()
    val second = build(attemptUp = {
      done.add(it.name)
      Result(ok(), it)
    })
    second.start() // recover() requeues the rows a killed process left running
    waitFor("both finished after restart") { done.size == 2 && store.all().all { it.state == State.DONE } }
    gate.countDown()
  }

  @Test fun offersAreDeduplicatedByOfferId() {
    val t = build(attemptUp = { Result(ok(), it) }, attemptDown = { Result(Outcome(error = IOException("slow"), progress = false), it) })
    t.start()
    t.onOffer("o1", "file", "a.txt", 5)
    t.onOffer("o1", "file", "a.txt", 5) // replayed on reconnect
    t.onOffer("o2", "dir", "folder", 0)
    assertEquals(2, store.all().size)
    assertEquals(setOf("file", "dir"), store.all().map { it.kind }.toSet())
  }

  @Test fun cancelStopsARunningTransferAndCleansUp() {
    val started = CountDownLatch(1)
    val discarded = ArrayList<String>()
    lateinit var t: Transfers
    t = build(discarded = discarded, attemptUp = {
      started.countDown()
      Thread.sleep(200)
      t.checkCancelledSafely(it.id)
      Result(ok(), it)
    })
    t.start()
    val row = up()
    t.enqueue(row)
    assertTrue(started.await(5, TimeUnit.SECONDS))
    t.cancel(row.id)
    waitFor("cancelled") { store.get(row.id)?.state == State.CANCELLED }
    Thread.sleep(300)
    assertEquals("a cancelled row must not be resurrected", State.CANCELLED, store.get(row.id)!!.state)
  }

  @Test fun failedRowsCanBeRetriedByTheUser() {
    val mode = AtomicInteger(0)
    val t = build(attemptUp = { if (mode.get() == 0) Result(Outcome(status = 413), it) else Result(ok(), it) })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("failed") { store.get(row.id)?.state == State.FAILED }
    mode.set(1)
    t.retry(row.id)
    waitFor("done") { store.get(row.id)?.state == State.DONE }
    assertNull(store.get(row.id)!!.error)
  }

  @Test fun failedDownloadsAreAckedAndDiscarded() {
    val discarded = ArrayList<String>()
    val t = build(discarded = discarded, attemptUp = { Result(ok(), it) }, attemptDown = { Result(Outcome(status = 422), it, "Unsafe path in folder: ../x") })
    t.start()
    t.onOffer("o1", "dir", "folder", 0)
    waitFor("failed") { store.all().firstOrNull()?.state == State.FAILED }
    val row = store.all().first()
    assertEquals("Unsafe path in folder: ../x", row.error)
    assertEquals(listOf(row.id), discarded)
  }

  @Test fun restartResetsTheOffsetOnce() {
    val offsets = ArrayList<Long>()
    val t = build(attemptUp = {
      offsets.add(it.offset)
      if (offsets.size == 1) {
        Result(Outcome(status = 412, ranged = true), it.copy(offset = 7))
      } else {
        Result(ok(), it)
      }
    })
    t.start()
    val row = up()
    t.enqueue(row)
    waitFor("done") { store.get(row.id)?.state == State.DONE }
    assertEquals(listOf(0L, 0L), offsets)
  }
}

private fun Transfers.checkCancelledSafely(id: String) = try {
  checkCancelled(id)
} catch (_: Cancelled) {
  throw Cancelled()
}
