package expo.modules.sharingservice

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

class RetryTest {
  private fun policy(clock: () -> Long = { 1_000L }) = Retry.Policy(jitter = { it }, clock = clock) // worst-case jitter

  @Test fun classifierTable() {
    val cases = listOf(
      Outcome(error = IOException("reset")) to Action.BACKOFF,
      Outcome(error = IOException("x"), unreachable = true) to Action.WAIT,
      Outcome(status = 408) to Action.BACKOFF,
      Outcome(status = 429) to Action.BACKOFF,
      Outcome(status = 502) to Action.BACKOFF,
      Outcome(status = 503) to Action.BACKOFF,
      Outcome(status = 504) to Action.BACKOFF,
      Outcome(status = 423) to Action.BACKOFF,
      Outcome(status = 500) to Action.BACKOFF,
      Outcome(status = 409, patch = true) to Action.NOW,
      Outcome(status = 409) to Action.PERMANENT,
      Outcome(status = 412, ranged = true) to Action.RESTART,
      Outcome(status = 416, ranged = true) to Action.RESTART,
      Outcome(status = 412) to Action.PERMANENT,
      Outcome(status = 401) to Action.AUTH,
      Outcome(status = 403) to Action.DROP,
      Outcome(status = 404) to Action.DROP,
      Outcome(status = 410) to Action.DROP,
      Outcome(status = 413) to Action.PERMANENT,
      Outcome(status = 422) to Action.PERMANENT,
      Outcome(status = 507) to Action.PERMANENT,
    )
    for ((o, want) in cases) assertEquals("$o", want, Retry.classify(o))
  }

  @Test fun backoffCeilingsDouble() {
    val p = policy()
    assertEquals(listOf(1, 2, 4, 8, 16, 32, 60, 60).map { it * 1000L }, (1..8).map { p.ceiling(it) })
  }

  @Test fun jitterStaysInsideTheCeiling() {
    val p = Retry.Policy()
    repeat(500) { n ->
      val c = p.ceiling(n % 10 + 1)
      val j = p.jitter(c)
      assertTrue(j in 0..c)
    }
  }

  @Test fun attemptsResetOnProgress() {
    val p = policy()
    val st = Retry.State()
    repeat(7) { assertEquals(Action.BACKOFF, p.next(st, Outcome(error = IOException("x"))).action) }
    val d = p.next(st, Outcome(error = IOException("x"), progress = true))
    assertEquals(Decision(Action.BACKOFF, 1000), d)
    repeat(7) { p.next(st, Outcome(error = IOException("x"))) }
    assertEquals(Action.GIVE_UP, p.next(st, Outcome(error = IOException("x"))).action)
  }

  @Test fun givesUpAfter24Hours() {
    var now = 1_000L
    val p = policy { now }
    val st = Retry.State()
    p.next(st, Outcome(error = IOException("x")))
    now += 25 * 3600_000L
    assertEquals(Action.GIVE_UP, p.next(st, Outcome(error = IOException("x"))).action)
  }

  @Test fun retryAfterIsHonoured() {
    val d = policy().next(Retry.State(), Outcome(status = 503, retryAfterMs = 30_000))
    assertEquals(Decision(Action.BACKOFF, 30_000), d)
    assertEquals(7_000L, Retry.retryAfterMs("7"))
    assertEquals(0L, Retry.retryAfterMs("soon"))
    assertEquals(0L, Retry.retryAfterMs("-3"))
    assertEquals(0L, Retry.retryAfterMs(null))
  }

  @Test fun restartIsAllowedOnce() {
    val p = policy()
    val st = Retry.State()
    assertEquals(Action.RESTART, p.next(st, Outcome(status = 412, ranged = true)).action)
    assertEquals(Action.PERMANENT, p.next(st, Outcome(status = 412, ranged = true)).action)
  }

  @Test fun unreachableLaptopWaitsWithoutSpendingAttempts() {
    val p = policy()
    val st = Retry.State()
    repeat(50) { assertEquals(Action.WAIT, p.next(st, Outcome(error = IOException("no route"), unreachable = true)).action) }
    assertEquals(0, st.attempts)
    var now = 1_000L
    val q = policy { now }
    val s2 = Retry.State()
    q.next(s2, Outcome(error = IOException("x"), unreachable = true))
    now += 25 * 3600_000L
    assertEquals(Action.GIVE_UP, q.next(s2, Outcome(error = IOException("x"), unreachable = true)).action)
  }

  @Test fun connectFailuresAreUnreachableButMidTransferFailuresAreNot() {
    assertTrue(isUnreachable(java.net.ConnectException()))
    assertTrue(isUnreachable(java.net.UnknownHostException()))
    assertTrue(isUnreachable(java.net.SocketTimeoutException("connect timed out")))
    assertTrue(!isUnreachable(java.net.SocketTimeoutException("timeout")))
    assertTrue(!isUnreachable(java.net.SocketException("Connection reset")))
  }
}
