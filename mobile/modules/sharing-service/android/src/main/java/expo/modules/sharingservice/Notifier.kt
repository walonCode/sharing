package expo.modules.sharingservice

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import java.util.concurrent.atomic.AtomicInteger

class Notifier(private val ctx: Context) {
  private val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
  private val ids = AtomicInteger(1000)

  init {
    nm.createNotificationChannel(NotificationChannel(SERVICE_CHANNEL, "Background sync", NotificationManager.IMPORTANCE_MIN))
    nm.createNotificationChannel(NotificationChannel(TRANSFER_CHANNEL, "Transfers", NotificationManager.IMPORTANCE_DEFAULT))
  }

  fun foreground(text: String): Notification =
    Notification.Builder(ctx, SERVICE_CHANNEL)
      .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
      .setContentTitle("Sharing")
      .setContentText(text)
      .setOngoing(true)
      .build()

  fun updateForeground(text: String) = nm.notify(FOREGROUND_ID, foreground(text))

  fun transfer(title: String, text: String) {
    val n = Notification.Builder(ctx, TRANSFER_CHANNEL)
      .setSmallIcon(android.R.drawable.stat_sys_download_done)
      .setContentTitle(title)
      .setContentText(text)
      .setAutoCancel(true)
      .build()
    nm.notify(ids.incrementAndGet(), n)
  }

  companion object {
    const val SERVICE_CHANNEL = "sharing_service"
    const val TRANSFER_CHANNEL = "sharing_transfers"
    const val FOREGROUND_ID = 1
  }
}
