package expo.modules.sharingservice

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.content.ContextCompat

/** Foreground service that holds the laptop connection open indefinitely. */
class SharingService : Service() {
  private var connection: Connection? = null
  private lateinit var notifier: Notifier
  private lateinit var prefs: Prefs

  override fun onBind(intent: Intent?): IBinder? = null

  override fun onCreate() {
    super.onCreate()
    prefs = Prefs(this)
    notifier = Notifier(this)
    val n = notifier.foreground("Connecting to your laptop")
    if (Build.VERSION.SDK_INT >= 29) {
      startForeground(Notifier.FOREGROUND_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
    } else {
      startForeground(Notifier.FOREGROUND_ID, n)
    }
    if (!prefs.paired) { stopSelf(); return }
    connection = Connection(this, prefs, notifier).also { it.start() }
    Status.running = true
    Status.listeners.add(statusListener)
    Status.changed()
  }

  private val statusListener: () -> Unit = {
    notifier.updateForeground(if (Status.connected) "Connected to ${prefs.laptopName}" else "Looking for your laptop")
  }

  override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
    val c = connection ?: return START_NOT_STICKY
    when (intent?.action) {
      ACTION_UPLOAD -> {
        val clip = intent.clipData
        val uris = (0 until (clip?.itemCount ?: 0)).mapNotNull { clip!!.getItemAt(it).uri }
        c.uploadUris(uris)
      }
      ACTION_UPLOAD_TREE -> intent.data?.let(c::uploadTree)
      ACTION_SEND_CLIP -> intent.getStringExtra(EXTRA_TEXT)?.let(c::sendClip)
      ACTION_CANCEL -> intent.getStringExtra(EXTRA_ID)?.let(c::cancel)
      ACTION_CANCEL_MANY -> intent.getStringArrayExtra(EXTRA_IDS)?.forEach(c::cancel)
      ACTION_RETRY -> intent.getStringExtra(EXTRA_ID)?.let(c::retry)
      ACTION_POKE -> c.poke()
    }
    return START_STICKY
  }

  override fun onDestroy() {
    connection?.stop()
    Status.listeners.remove(statusListener)
    Status.running = false
    Status.connected = false
    Status.changed()
    super.onDestroy()
  }

  companion object {
    const val ACTION_UPLOAD = "expo.modules.sharingservice.UPLOAD"
    const val ACTION_UPLOAD_TREE = "expo.modules.sharingservice.UPLOAD_TREE"
    const val ACTION_SEND_CLIP = "expo.modules.sharingservice.SEND_CLIP"
    const val ACTION_CANCEL = "expo.modules.sharingservice.CANCEL"
    const val ACTION_CANCEL_MANY = "expo.modules.sharingservice.CANCEL_MANY"
    const val ACTION_RETRY = "expo.modules.sharingservice.RETRY"
    const val ACTION_POKE = "expo.modules.sharingservice.POKE"
    const val EXTRA_TEXT = "text"
    const val EXTRA_ID = "id"
    const val EXTRA_IDS = "ids"

    fun intent(ctx: Context, action: String): Intent = Intent(ctx, SharingService::class.java).setAction(action)

    fun start(ctx: Context) = ContextCompat.startForegroundService(ctx, Intent(ctx, SharingService::class.java))
    fun stop(ctx: Context) { ctx.stopService(Intent(ctx, SharingService::class.java)) }

    fun send(ctx: Context, action: String, configure: Intent.() -> Unit) {
      ContextCompat.startForegroundService(ctx, intent(ctx, action).apply(configure))
    }
  }
}
