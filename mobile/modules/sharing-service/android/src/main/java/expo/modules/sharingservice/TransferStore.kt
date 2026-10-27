package expo.modules.sharingservice

import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.sqlite.SQLiteDatabase
import org.json.JSONObject
import java.util.UUID

object Dir {
  const val UP = "up"
  const val DOWN = "down"
}

object State {
  const val QUEUED = "queued" // waiting for its turn or for nextRetryAt
  const val RUNNING = "running"
  const val DONE = "done"
  const val FAILED = "failed" // gave up or permanent error; the user may retry
  const val CANCELLED = "cancelled"
}

/** One row of the persistent transfer queue. */
data class Transfer(
  val id: String = UUID.randomUUID().toString(),
  val direction: String,
  val name: String,
  /** "file" or "dir". */
  val kind: String,
  /** Upload: content/tree URI or spool path. Download: the laptop's offer id. */
  val source: String,
  val size: Long = -1,
  val offset: Long = 0,
  val state: String = State.QUEUED,
  val attempts: Int = 0,
  val startedAt: Long = 0,
  val nextRetryAt: Long = 0,
  /** Upload: the laptop's upload id. Download: the pending MediaStore URI. */
  val remoteId: String? = null,
  val error: String? = null,
  val createdAt: Long = System.currentTimeMillis(),
  /** Small JSON blob for per-kind progress (folder index, etag, spool path). */
  val extra: JSONObject = JSONObject(),
) {
  val finished: Boolean get() = state == State.DONE || state == State.CANCELLED
  val terminal: Boolean get() = finished || state == State.FAILED
}

/** Persistence for the queue. SQLite on the phone, in memory in tests. */
interface TransferStore {
  fun put(t: Transfer)
  fun get(id: String): Transfer?
  fun all(): List<Transfer>
  fun delete(id: String)

  /** Rows that should run now: queued with nextRetryAt <= [now], oldest first. */
  fun due(now: Long): List<Transfer>

  /** Marks rows left RUNNING by a killed process as queued again. */
  fun recover()

  /** URIs created by a download (so a failed folder deletes only its own files). */
  fun addArtifact(transferId: String, uri: String)
  fun artifacts(transferId: String): List<String>
  fun clearArtifacts(transferId: String)

  /** Deletes finished rows older than [olderThan]. */
  fun prune(olderThan: Long)
}

class MemoryTransferStore : TransferStore {
  private val rows = LinkedHashMap<String, Transfer>()
  private val arts = HashMap<String, MutableList<String>>()

  @Synchronized override fun put(t: Transfer) {
    rows[t.id] = t
  }

  @Synchronized override fun get(id: String) = rows[id]

  @Synchronized override fun all() = rows.values.toList()

  @Synchronized override fun delete(id: String) {
    rows.remove(id)
    arts.remove(id)
  }

  @Synchronized override fun due(now: Long) = rows.values.filter { it.state == State.QUEUED && it.nextRetryAt <= now }.sortedBy { it.createdAt }

  @Synchronized override fun recover() {
    rows.replaceAll { _, t -> if (t.state == State.RUNNING) t.copy(state = State.QUEUED) else t }
  }

  @Synchronized override fun addArtifact(transferId: String, uri: String) {
    arts.getOrPut(transferId) { ArrayList() }.add(uri)
  }

  @Synchronized override fun artifacts(transferId: String) = arts[transferId]?.toList() ?: emptyList()

  @Synchronized override fun clearArtifacts(transferId: String) {
    arts.remove(transferId)
  }

  @Synchronized override fun prune(olderThan: Long) {
    rows.values.filter { it.terminal && it.createdAt < olderThan }.map { it.id }.forEach { delete(it) }
  }
}

/**
 * SQLite queue in the no-backup directory, so it is neither included in backups
 * nor restored onto another device where the URIs and offsets mean nothing.
 */
class SqliteTransferStore(ctx: Context) : TransferStore {
  // Opened by path in noBackupFilesDir (SQLiteOpenHelper would put it in the backed-up databases dir).
  private val db: SQLiteDatabase = SQLiteDatabase.openOrCreateDatabase(
    java.io.File(ctx.applicationContext.noBackupFilesDir, "transfers.db").path,
    null,
  ).also { d ->
    if (d.version == 0) {
      d.execSQL(
        """CREATE TABLE transfers (
          id TEXT PRIMARY KEY, direction TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, source TEXT NOT NULL,
          size INTEGER NOT NULL, off INTEGER NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL,
          startedAt INTEGER NOT NULL, nextRetryAt INTEGER NOT NULL, remoteId TEXT, error TEXT,
          createdAt INTEGER NOT NULL, extra TEXT NOT NULL)""",
      )
      d.execSQL("CREATE TABLE artifacts (transferId TEXT NOT NULL, uri TEXT NOT NULL)")
      d.execSQL("CREATE INDEX artifacts_by_transfer ON artifacts(transferId)")
      d.version = 1
    }
    d.enableWriteAheadLogging()
  }

  private fun cv(t: Transfer) = ContentValues().apply {
    put("id", t.id)
    put("direction", t.direction)
    put("name", t.name)
    put("kind", t.kind)
    put("source", t.source)
    put("size", t.size)
    put("off", t.offset)
    put("state", t.state)
    put("attempts", t.attempts)
    put("startedAt", t.startedAt)
    put("nextRetryAt", t.nextRetryAt)
    put("remoteId", t.remoteId)
    put("error", t.error)
    put("createdAt", t.createdAt)
    put("extra", t.extra.toString())
  }

  private fun read(c: Cursor) = Transfer(
    id = c.getString(c.getColumnIndexOrThrow("id")), direction = c.getString(c.getColumnIndexOrThrow("direction")),
    name = c.getString(c.getColumnIndexOrThrow("name")), kind = c.getString(c.getColumnIndexOrThrow("kind")),
    source = c.getString(c.getColumnIndexOrThrow("source")), size = c.getLong(c.getColumnIndexOrThrow("size")),
    offset = c.getLong(c.getColumnIndexOrThrow("off")), state = c.getString(c.getColumnIndexOrThrow("state")),
    attempts = c.getInt(c.getColumnIndexOrThrow("attempts")), startedAt = c.getLong(c.getColumnIndexOrThrow("startedAt")),
    nextRetryAt = c.getLong(c.getColumnIndexOrThrow("nextRetryAt")), remoteId = c.getString(c.getColumnIndexOrThrow("remoteId")),
    error = c.getString(c.getColumnIndexOrThrow("error")), createdAt = c.getLong(c.getColumnIndexOrThrow("createdAt")),
    extra = runCatching { JSONObject(c.getString(c.getColumnIndexOrThrow("extra"))) }.getOrDefault(JSONObject()),
  )

  private fun query(where: String?, args: Array<String>?, order: String? = "createdAt ASC"): List<Transfer> =
    db.query("transfers", null, where, args, null, null, order).use { c -> buildList { while (c.moveToNext()) add(read(c)) } }

  @Synchronized override fun put(t: Transfer) {
    db.insertWithOnConflict("transfers", null, cv(t), SQLiteDatabase.CONFLICT_REPLACE)
  }

  @Synchronized override fun get(id: String) = query("id = ?", arrayOf(id)).firstOrNull()

  @Synchronized override fun all() = query(null, null)

  @Synchronized override fun delete(id: String) {
    db.delete("transfers", "id = ?", arrayOf(id))
    db.delete("artifacts", "transferId = ?", arrayOf(id))
  }

  @Synchronized override fun due(now: Long) = query("state = ? AND nextRetryAt <= ?", arrayOf(State.QUEUED, now.toString()))

  @Synchronized override fun recover() {
    db.execSQL("UPDATE transfers SET state = ? WHERE state = ?", arrayOf(State.QUEUED, State.RUNNING))
  }

  @Synchronized override fun addArtifact(transferId: String, uri: String) {
    db.insert(
      "artifacts",
      null,
      ContentValues().apply {
        put("transferId", transferId)
        put("uri", uri)
      },
    )
  }

  @Synchronized override fun artifacts(transferId: String): List<String> = db.query(
    "artifacts",
    arrayOf("uri"),
    "transferId = ?",
    arrayOf(transferId),
    null,
    null,
    null,
  )
    .use { c -> buildList { while (c.moveToNext()) add(c.getString(0)) } }

  @Synchronized override fun clearArtifacts(transferId: String) {
    db.delete("artifacts", "transferId = ?", arrayOf(transferId))
  }

  @Synchronized override fun prune(olderThan: Long) {
    query("createdAt < ?", arrayOf(olderThan.toString())).filter { it.terminal }.forEach { delete(it.id) }
  }
}
