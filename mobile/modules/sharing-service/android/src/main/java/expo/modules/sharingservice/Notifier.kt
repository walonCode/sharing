package expo.modules.sharingservice

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import java.util.concurrent.atomic.AtomicInteger

/**
 * Notifications: the ongoing service notification, one progress notification for
 * the active transfer (with Cancel), one running summary per direction instead of
 * a notification per file, and a failure notification with Retry.
 */
class Notifier(private val ctx: Context) : TransferEvents {
  private val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
  private val ids = AtomicInteger(2000)

  private var lastProgressAt = 0L
  private var received = 0
  private var sent = 0

  init {
    nm.createNotificationChannel(NotificationChannel(SERVICE_CHANNEL, "Background sync", NotificationManager.IMPORTANCE_LOW))
    nm.createNotificationChannel(NotificationChannel(TRANSFER_CHANNEL, "Transfers", NotificationManager.IMPORTANCE_DEFAULT))
    nm.createNotificationChannel(NotificationChannel(PROGRESS_CHANNEL, "Transfer progress", NotificationManager.IMPORTANCE_LOW))
  }

  fun foreground(text: String): Notification =
    Notification.Builder(ctx, SERVICE_CHANNEL)
      .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
      .setContentTitle("Sharing")
      .setContentText(text)
      .setOngoing(true)
      .addAction(sendClipboardAction())
      .build()

  /** Android 10+ only lets a focused app read the clipboard, so a tap here (which focuses a transparent activity) is the way in. */
  private fun sendClipboardAction(): Notification.Action {
    val intent = Intent(ctx, ClipboardSendActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    val pending = PendingIntent.getActivity(ctx, 0, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    return Notification.Action.Builder(null, "Send clipboard to laptop", pending).build()
  }

  fun updateForeground(text: String) = nm.notify(FOREGROUND_ID, foreground(text))

  private fun serviceAction(action: String, id: String, label: String, code: Int): Notification.Action {
    val i = SharingService.intent(ctx, action).putExtra(SharingService.EXTRA_ID, id)
    val p = PendingIntent.getForegroundService(ctx, code, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    return Notification.Action.Builder(null, label, p).build()
  }

  /** One-off message (pairing problems, rejected shares). */
  fun transfer(title: String, text: String) {
    val n = Notification.Builder(ctx, TRANSFER_CHANNEL)
      .setSmallIcon(android.R.drawable.stat_sys_download_done)
      .setContentTitle(title)
      .setContentText(text)
      .setAutoCancel(true)
      .build()
    nm.notify(ids.incrementAndGet(), n)
  }

  /** "Sending N items" with an Undo that cancels them all during the grace period. */
  fun undoable(count: Int, laptop: String, transferIds: List<String>) {
    val i = SharingService.intent(ctx, SharingService.ACTION_CANCEL_MANY).putExtra(SharingService.EXTRA_IDS, transferIds.toTypedArray())
    val p = PendingIntent.getForegroundService(ctx, UNDO_CODE, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    val n = Notification.Builder(ctx, TRANSFER_CHANNEL)
      .setSmallIcon(android.R.drawable.stat_sys_upload)
      .setContentTitle("Sending $count item${if (count == 1) "" else "s"} to ${laptop}")
      .setContentText("Tap Undo within a few seconds to cancel")
      .setTimeoutAfter(Connection.UNDO_MS)
      .addAction(Notification.Action.Builder(null, "Undo", p).build())
      .build()
    nm.notify(UNDO_ID, n)
  }

  // ---- TransferEvents ----

  override fun progress(t: Transfer, bytes: Long) {
    val now = System.currentTimeMillis()
    if (now - lastProgressAt < 1_000) return
    lastProgressAt = now
    val verb = if (t.direction == Dir.UP) "Sending" else "Receiving"
    val b = Notification.Builder(ctx, PROGRESS_CHANNEL)
      .setSmallIcon(if (t.direction == Dir.UP) android.R.drawable.stat_sys_upload else android.R.drawable.stat_sys_download)
      .setContentTitle("$verb ${t.name}")
      .setOngoing(true)
      .setOnlyAlertOnce(true)
      .addAction(serviceAction(SharingService.ACTION_CANCEL, t.id, "Cancel", 1))
    if (t.size > 0) {
      val pct = (bytes * 100 / t.size).toInt().coerceIn(0, 100)
      b.setProgress(100, pct, false).setContentText("$pct%")
    } else b.setProgress(0, 0, true)
    nm.notify(PROGRESS_ID, b.build())
  }

  override fun changed(t: Transfer) {
    when (t.state) {
      State.DONE -> {
        nm.cancel(PROGRESS_ID)
        if (t.direction == Dir.DOWN) { received++; summary(RECEIVED_ID, "Received", received, t.name) }
        else { sent++; summary(SENT_ID, "Sent", sent, t.name) }
      }
      State.FAILED -> {
        nm.cancel(PROGRESS_ID)
        val n = Notification.Builder(ctx, TRANSFER_CHANNEL)
          .setSmallIcon(android.R.drawable.stat_notify_error)
          .setContentTitle(if (t.direction == Dir.UP) "Send failed" else "Receive failed")
          .setContentText("${t.name}: ${t.error ?: "unknown error"}")
          .setAutoCancel(true)
          .addAction(serviceAction(SharingService.ACTION_RETRY, t.id, "Retry", 2))
          .build()
        nm.notify(FAIL_BASE + (t.id.hashCode() and 0xffff), n)
      }
      State.CANCELLED -> nm.cancel(PROGRESS_ID)
      else -> {}
    }
  }

  /** One notification per direction whose count grows ("Received 12 items"), not one per file. */
  private fun summary(id: Int, verb: String, count: Int, last: String) {
    val n = Notification.Builder(ctx, TRANSFER_CHANNEL)
      .setSmallIcon(android.R.drawable.stat_sys_download_done)
      .setContentTitle(if (count == 1) "$verb $last" else "$verb $count items")
      .setContentText(if (count == 1) "" else "Latest: $last")
      .setAutoCancel(true)
      .setDeleteIntent(null)
      .build()
    nm.notify(id, n)
  }

  /** Resets the running summaries once the user has seen them (called when the queue goes idle). */
  fun resetCounts() { received = 0; sent = 0 }

  override fun authFailed() {
    transfer("Pair again", "The laptop no longer trusts this phone")
  }

  companion object {
    const val SERVICE_CHANNEL = "sharing_service_v2"
    const val TRANSFER_CHANNEL = "sharing_transfers"
    const val PROGRESS_CHANNEL = "sharing_progress"
    const val FOREGROUND_ID = 1
    const val PROGRESS_ID = 2
    const val UNDO_ID = 3
    const val RECEIVED_ID = 4
    const val SENT_ID = 5
    const val FAIL_BASE = 100_000
    private const val UNDO_CODE = 3
  }
}
