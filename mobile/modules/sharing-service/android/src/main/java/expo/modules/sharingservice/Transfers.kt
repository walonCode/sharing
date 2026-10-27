package expo.modules.sharingservice

import java.io.IOException
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/** Observer for the notification and the status bridge. Called from worker threads. */
interface TransferEvents {
  /** [bytes] of the transfer's total have moved. Called often; implementations throttle. */
  fun progress(t: Transfer, bytes: Long) {}

  /** The row changed state (queued, running, retry scheduled, done, failed, dropped). */
  fun changed(t: Transfer) {}

  /** The laptop rejected the device token: the user must pair again. */
  fun authFailed() {}
}

class Cancelled : IOException("cancelled")

/**
 * The persistent transfer queue and its workers. A fixed number of workers (two
 * by default, not one thread per file) runs due rows; each row is retried with
 * [Retry.Policy] and survives process death because the [TransferStore] holds
 * everything needed to continue.
 */
class Transfers(
  private val store: TransferStore,
  /** One attempt of an upload row ([Uploader.attempt]). */
  private val attemptUp: (Transfer) -> Result,
  /** One attempt of a download row ([Downloader.attempt]). */
  private val attemptDown: (Transfer) -> Result,
  /** Removes what a failed or cancelled download created ([Downloader.discard]). */
  private val discardDown: (Transfer) -> Unit,
  private val events: TransferEvents,
  private val policy: Retry.Policy = Retry.Policy(),
  private val workers: Int = 2,
  private val clock: () -> Long = System::currentTimeMillis,
  /** Best-effort cleanup of a laptop-side upload session or offer after a cancel. */
  private val remote: () -> Remote? = { null },
  private val onFinished: (Transfer) -> Unit = {},
) {
  private val lock = Object()
  private val running = HashMap<String, AtomicBoolean>() // id -> cancel flag
  private var pool: ExecutorService? = null
  private var scheduler: Thread? = null

  @Volatile private var stopped = true

  fun start() {
    if (!stopped) return
    stopped = false
    store.recover()
    store.prune(clock() - RETENTION_MS)
    pool = Executors.newFixedThreadPool(workers) { r -> Thread(r, "sharing-transfer").also { it.isDaemon = true } }
    scheduler = Thread(::schedule, "sharing-queue").also {
      it.isDaemon = true
      it.start()
    }
  }

  fun stop() {
    stopped = true
    synchronized(lock) {
      running.values.forEach { it.set(true) }
      lock.notifyAll()
    }
    pool?.shutdownNow()
  }

  /** Wakes the scheduler: new work or a changed condition. */
  fun poke() = synchronized(lock) { lock.notifyAll() }

  /** A network came back or the laptop connected: skip every waiting delay. */
  fun retryNow() {
    store.all().filter { it.state == State.QUEUED && it.nextRetryAt > 0 }.forEach { store.put(it.copy(nextRetryAt = 0)) }
    poke()
  }

  fun enqueue(t: Transfer) {
    store.put(t)
    events.changed(t)
    poke()
  }

  /** A download offer from the laptop; replays of an id already known are ignored. */
  fun onOffer(id: String, kind: String, name: String, size: Long) {
    if (store.all().any { it.direction == Dir.DOWN && it.source == id }) return
    enqueue(Transfer(direction = Dir.DOWN, name = Names.sanitize(name), kind = if (kind == "dir") "dir" else "file", source = id, size = size))
  }

  /** The laptop cancelled an offer. */
  fun onLaptopCancel(id: String) {
    store.all().firstOrNull { it.direction == Dir.DOWN && it.source == id && !it.terminal }?.let { cancel(it.id, notifyLaptop = false) }
  }

  fun cancel(id: String, notifyLaptop: Boolean = true) {
    val t = store.get(id) ?: return
    synchronized(lock) { running[id]?.set(true) }
    val done = t.copy(state = State.CANCELLED, error = null)
    store.put(done)
    cleanup(done, notifyLaptop)
    events.changed(done)
    poke()
  }

  fun retry(id: String) {
    val t = store.get(id) ?: return
    if (t.state != State.FAILED) return
    val fresh = t.copy(state = State.QUEUED, attempts = 0, startedAt = 0, nextRetryAt = 0, error = null)
    store.put(fresh)
    events.changed(fresh)
    poke()
  }

  fun snapshot(): List<Transfer> = store.all()

  /** Throws [Cancelled] if the row is being cancelled (or the queue is stopping); progress callbacks call it to abort a transfer mid-stream. */
  fun checkCancelled(id: String) {
    if (stopped || synchronized(lock) { running[id]?.get() } == true) throw Cancelled()
  }

  // ---- scheduler ----

  private fun schedule() {
    while (!stopped) {
      val now = clock()
      val toRun = synchronized(lock) {
        val free = workers - running.size
        store.due(now).filter { it.id !in running }.take(maxOf(free, 0)).onEach { running[it.id] = AtomicBoolean(false) }
      }
      for (t in toRun) pool?.execute { runRow(t) }
      synchronized(lock) {
        if (stopped) return
        val next = store.all().filter { it.state == State.QUEUED && it.id !in running }.minOfOrNull { it.nextRetryAt }
        val wait = if (next == null) IDLE_WAIT_MS else (next - clock()).coerceIn(50, IDLE_WAIT_MS)
        if (toRun.isEmpty() || running.size >= workers) lock.wait(wait)
      }
    }
  }

  private fun runRow(t0: Transfer) {
    val cancelFlag = synchronized(lock) { running[t0.id] } ?: return
    try {
      var row = t0.copy(state = State.RUNNING, nextRetryAt = 0)
      store.put(row)
      events.changed(row)
      val st = Retry.State(row.attempts, row.startedAt, row.extra.optBoolean("restarted"))
      while (!stopped && !cancelFlag.get()) {
        val res = try {
          if (row.direction == Dir.UP) attemptUp(row) else attemptDown(row)
        } catch (e: Cancelled) {
          break
        } catch (e: Exception) {
          Result(Outcome(error = e), store.get(row.id) ?: row, e.message)
        }
        if (cancelFlag.get()) break
        row = store.get(row.id) ?: break
        if (row.state == State.CANCELLED) break
        row = res.transfer.takeIf { it.id == row.id } ?: row
        if (res.outcome.ok) {
          finish(row, State.DONE, null)
          break
        }

        val d = policy.next(st, res.outcome)
        row = row.copy(attempts = st.attempts, startedAt = st.startedAt)
        when (d.action) {
          Action.NOW -> {
            store.put(row)
          }
          Action.RESTART -> {
            row = restart(row)
            st.restarted = true
            row = row.copy(extra = copyExtra(row).put("restarted", true))
            store.put(row)
          }
          Action.BACKOFF, Action.WAIT -> {
            val why = if (d.action == Action.WAIT) "Waiting for your laptop" else (res.message ?: describe(res.outcome))
            row = row.copy(state = State.QUEUED, nextRetryAt = clock() + d.delayMs, error = why)
            store.put(row)
            events.changed(row)
            break
          }
          Action.AUTH -> {
            finish(row, State.FAILED, "Laptop no longer trusts this phone; pair again")
            events.authFailed()
            break
          }
          Action.DROP -> {
            cleanup(row, notifyLaptop = false)
            store.delete(row.id)
            events.changed(row.copy(state = State.CANCELLED, error = res.message ?: "The laptop no longer has this item"))
            break
          }
          Action.PERMANENT, Action.GIVE_UP -> {
            val why = res.message ?: if (d.action == Action.GIVE_UP) "Gave up after repeated failures: ${describe(res.outcome)}" else describe(res.outcome)
            finish(row, State.FAILED, why)
            break
          }
        }
      }
    } finally {
      synchronized(lock) {
        running.remove(t0.id)
        lock.notifyAll()
      }
    }
  }

  private fun copyExtra(t: Transfer) = org.json.JSONObject(t.extra.toString())

  /** Throws the upload session away (or truncates the download) so the next attempt starts at byte zero. */
  private fun restart(t: Transfer): Transfer {
    if (t.direction == Dir.UP) {
      t.remoteId?.let { id -> remote()?.deleteUpload(id) }
      val keep = org.json.JSONObject()
      return t.copy(remoteId = null, offset = 0, extra = keep)
    }
    return t.copy(offset = 0)
  }

  private fun finish(t: Transfer, state: String, error: String?) {
    val done = t.copy(state = state, error = error, offset = if (state == State.DONE && t.size >= 0) t.size else t.offset)
    store.put(done)
    if (state == State.DONE) {
      store.clearArtifacts(done.id)
      if (done.direction == Dir.UP) cleanupSpool(done)
    } else if (done.direction == Dir.DOWN) {
      // Tell the laptop it failed and remove only what this transfer created.
      remote()?.ack(done.source, "failed", error)
      discardDown(done)
    } else {
      done.remoteId?.let { remote()?.deleteUpload(it) }
    }
    events.changed(done)
    onFinished(done)
  }

  private fun cleanup(t: Transfer, notifyLaptop: Boolean) {
    if (t.direction == Dir.DOWN) {
      if (notifyLaptop) remote()?.ack(t.source, "cancelled")
      discardDown(t)
    } else {
      t.remoteId?.let { remote()?.deleteUpload(it) }
      cleanupSpool(t)
    }
  }

  private var spoolCleaner: (Transfer) -> Unit = {}
  fun onSpoolCleanup(f: (Transfer) -> Unit) {
    spoolCleaner = f
  }
  private fun cleanupSpool(t: Transfer) = spoolCleaner(t)

  companion object {
    const val RETENTION_MS = 7L * 24 * 3600_000
    const val IDLE_WAIT_MS = 60_000L

    fun describe(o: Outcome): String = when {
      o.error != null -> o.error.message ?: o.error.javaClass.simpleName
      o.status == 413 -> "Too large for the laptop's limit"
      o.status == 422 -> "The laptop rejected the item"
      o.status == 507 -> "The laptop's disk is full"
      o.status == 401 -> "Laptop no longer trusts this phone; pair again"
      else -> "Laptop answered ${o.status}"
    }
  }
}
