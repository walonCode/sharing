package expo.modules.sharingservice

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Restarts the service after reboot or app update if the user left it enabled. */
class BootReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    val prefs = Prefs(ctx)
    if (prefs.paired && prefs.enabled) SharingService.start(ctx)
  }
}
