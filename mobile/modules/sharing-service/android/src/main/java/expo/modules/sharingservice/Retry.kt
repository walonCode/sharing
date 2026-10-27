package expo.modules.sharingservice

import kotlin.random.Random

/** What to do after a failed attempt. Mirrors api/internal/retry. */
enum class Action {
  BACKOFF, NOW, RESTART, AUTH, DROP, PERMANENT, GIVE_UP,
  /** The laptop cannot be reached at all: wait without spending attempts. */
  WAIT,
}

/** The result of one attempt, reduced to what the retry policy needs. */
data class Outcome(
  val status: Int = 0,
  val error: Throwable? = null,
  val retryAfterMs: Long = 0,
  /** The stored offset advanced during this attempt. */
  val progress: Boolean = false,
  /** The request was a PATCH of an upload (a 409 then carries the right offset). */
  val patch: Boolean = false,
  /** The request carried a Range header. */
  val ranged: Boolean = false,
  /** The laptop could not be reached (no connection was made), as opposed to a transfer that broke. */
  val unreachable: Boolean = false,
) {
  val ok: Boolean get() = error == null && status in 200..299
}

data class Decision(val action: Action, val delayMs: Long = 0)

/** One retry policy for every network call. The table is documented in docs/plan3.md. */
object Retry {
  fun classify(o: Outcome): Action {
    if (o.unreachable) return Action.WAIT
    if (o.error != null) return Action.BACKOFF // IO error, timeout, reset, stall
    return when (val s = o.status) {
      408, 429, 502, 503, 504, 423 -> Action.BACKOFF
      409 -> if (o.patch) Action.NOW else Action.PERMANENT
      412, 416 -> if (o.ranged) Action.RESTART else Action.PERMANENT
      401 -> Action.AUTH
      403, 404, 410 -> Action.DROP
      413, 422, 507 -> Action.PERMANENT
      else -> if (s >= 500) Action.BACKOFF else Action.PERMANENT
    }
  }

  /** Seconds-valued Retry-After header to milliseconds; anything else is zero. */
  fun retryAfterMs(header: String?): Long = header?.trim()?.toLongOrNull()?.takeIf { it >= 0 }?.times(1000) ?: 0

  class Policy(
    val baseMs: Long = 1_000,
    val capMs: Long = 60_000,
    val maxAttempts: Int = 8,
    val maxElapsedMs: Long = 24 * 3600_000L,
    /** Returns a value in [0, max]; tests inject a fixed source. */
    val jitter: (Long) -> Long = { max -> if (max <= 0) 0 else Random.nextLong(max + 1) },
    val clock: () -> Long = System::currentTimeMillis,
    /** How long to wait before looking for an unreachable laptop again (a network or reconnect event skips it). */
    val unreachableDelayMs: Long = 5 * 60_000L,
  ) {
    /** The un-jittered delay for the nth consecutive failure (n >= 1). */
    fun ceiling(n: Int): Long {
      var d = baseMs
      var i = 1
      while (i < n && d < capMs) { d *= 2; i++ }
      return minOf(d, capMs)
    }

    /** Classifies [o], updates [st], and says what to do next. Attempts reset on progress. */
    fun next(st: State, o: Outcome): Decision {
      if (st.startedAt == 0L) st.startedAt = clock()
      if (o.ok) return Decision(Action.NOW)
      val action = classify(o)
      if (o.progress) st.attempts = 0
      return when (action) {
        Action.RESTART -> if (st.restarted) Decision(Action.PERMANENT) else { st.restarted = true; Decision(Action.RESTART) }
        Action.BACKOFF -> {
          st.attempts++
          if (st.attempts > maxAttempts || clock() - st.startedAt > maxElapsedMs) Decision(Action.GIVE_UP)
          else Decision(Action.BACKOFF, maxOf(jitter(ceiling(st.attempts)), o.retryAfterMs))
        }
        Action.WAIT -> {
          // Does not count as an attempt: the laptop being off is not the transfer's fault.
          if (clock() - st.startedAt > maxElapsedMs) Decision(Action.GIVE_UP) else Decision(Action.WAIT, unreachableDelayMs)
        }
        Action.NOW -> {
          st.attempts++
          if (st.attempts > maxAttempts) Decision(Action.GIVE_UP) else Decision(Action.NOW)
        }
        else -> Decision(action)
      }
    }
  }

  /** Per-item bookkeeping between attempts. Persisted columns: attempts, startedAt. */
  class State(var attempts: Int = 0, var startedAt: Long = 0, var restarted: Boolean = false)
}

/** True for failures that mean no connection to the laptop was made. */
fun isUnreachable(e: Throwable): Boolean = when (e) {
  is java.net.ConnectException, is java.net.NoRouteToHostException, is java.net.UnknownHostException -> true
  is java.net.SocketTimeoutException -> e.message?.contains("connect", ignoreCase = true) == true
  else -> false
}
