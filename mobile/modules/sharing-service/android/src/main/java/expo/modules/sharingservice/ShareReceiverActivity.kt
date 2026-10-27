package expo.modules.sharingservice

import android.app.Activity
import android.content.ClipData
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.widget.Toast

/** Target of the system share sheet: forwards the shared items to the service and closes. */
class ShareReceiverActivity : Activity() {
  override fun onCreate(savedInstanceState: Bundle?) {
    super.onCreate(savedInstanceState)
    try {
      forward()
    } finally {
      finish()
    }
  }

  @Suppress("DEPRECATION")
  private fun forward() {
    if (!Prefs(this).paired) {
      Toast.makeText(this, "Open Sharing once to pair with your laptop", Toast.LENGTH_LONG).show()
      return
    }
    val shared: List<Uri> = when (intent.action) {
      Intent.ACTION_SEND -> listOfNotNull(intent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM))
      Intent.ACTION_SEND_MULTIPLE -> intent.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM).orEmpty()
      else -> emptyList()
    }
    val uris = shared.filter { it.scheme == "content" }
    if (uris.size < shared.size) {
      // file:// links would let another app make us read files we can access but it cannot.
      Toast.makeText(this, "Skipped ${shared.size - uris.size} item(s) that are not shared content", Toast.LENGTH_LONG).show()
      if (uris.isEmpty()) return
    }
    if (uris.isEmpty()) {
      // Plain text shares go through the clipboard channel.
      intent.getStringExtra(Intent.EXTRA_TEXT)?.let { text ->
        SharingService.send(this, SharingService.ACTION_SEND_CLIP) { putExtra(SharingService.EXTRA_TEXT, text) }
        Toast.makeText(this, "Sent to laptop clipboard", Toast.LENGTH_SHORT).show()
      }
      return
    }
    SharingService.send(this, SharingService.ACTION_UPLOAD) {
      // clipData carries the read grants to the service.
      clipData = ClipData.newRawUri("shared", uris[0]).also { c -> uris.drop(1).forEach { c.addItem(ClipData.Item(it)) } }
      addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    Toast.makeText(this, "Sending ${uris.size} item(s) to laptop", Toast.LENGTH_SHORT).show()
  }
}
