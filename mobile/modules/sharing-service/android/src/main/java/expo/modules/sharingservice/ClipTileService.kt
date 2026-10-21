package expo.modules.sharingservice

import android.app.PendingIntent
import android.content.Intent
import android.os.Build
import android.service.quicksettings.TileService

/** Quick-settings tile: tap to push the phone's clipboard to the laptop. */
class ClipTileService : TileService() {
  @Suppress("DEPRECATION")
  override fun onClick() {
    val intent = Intent(this, ClipboardSendActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    if (Build.VERSION.SDK_INT >= 34) {
      startActivityAndCollapse(PendingIntent.getActivity(this, 0, intent, PendingIntent.FLAG_IMMUTABLE))
    } else {
      startActivityAndCollapse(intent)
    }
  }
}
