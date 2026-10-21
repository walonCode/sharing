package expo.modules.sharingservice

import android.app.Activity
import android.content.ClipboardManager
import android.content.Context
import android.os.Bundle
import android.widget.Toast

/**
 * Android 10+ only lets the focused app read the clipboard, so a transparent
 * activity (launched from the quick-settings tile) reads it the moment it gains
 * focus, hands it to the service, and finishes.
 */
class ClipboardSendActivity : Activity() {
  private var done = false

  override fun onCreate(savedInstanceState: Bundle?) {
    super.onCreate(savedInstanceState)
    if (!Prefs(this).paired) finish()
  }

  override fun onWindowFocusChanged(hasFocus: Boolean) {
    super.onWindowFocusChanged(hasFocus)
    if (!hasFocus || done) return
    done = true
    val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    val text = cm.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty()
    if (text.isNotEmpty()) {
      SharingService.send(this, SharingService.ACTION_SEND_CLIP) { putExtra(SharingService.EXTRA_TEXT, text) }
      Toast.makeText(this, "Clipboard sent to laptop", Toast.LENGTH_SHORT).show()
    }
    finish()
  }
}
