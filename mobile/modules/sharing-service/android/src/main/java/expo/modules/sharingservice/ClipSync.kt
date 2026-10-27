package expo.modules.sharingservice

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Handler
import android.os.Looper
import org.json.JSONObject

/**
 * Two-way clipboard text sync over the session's websocket. Last writer wins; a
 * value we just applied is never echoed back. A clip that could not be sent (socket
 * down) is kept and sent after reconnect - the newest only.
 */
class ClipSync(ctx: Context, private val send: (String) -> Boolean) {
  private val main = Handler(Looper.getMainLooper())
  private val clipboard = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager

  @Volatile private var last = ""
  @Volatile private var lastTs = 0L
  @Volatile private var pending: String? = null

  /** A clip from the laptop. */
  fun onRemote(data: String, ts: Long) {
    if (data.isEmpty() || data.length > MAX_CHARS || data == last || ts < lastTs) return
    last = data
    lastTs = ts
    main.post { clipboard.setPrimaryClip(ClipData.newPlainText("sharing", data)) }
  }

  /** A clip from this phone. */
  fun sendLocal(text: String) {
    if (text.isEmpty() || text == last) return
    last = text
    lastTs = System.currentTimeMillis()
    val msg = JSONObject().put("type", "clip").put("mime", "text/plain").put("data", text).put("ts", lastTs).toString()
    pending = if (send(msg)) null else text
  }

  /** The socket is back: send the newest clip that was waiting. */
  fun onConnected() {
    val text = pending ?: return
    pending = null
    val msg = JSONObject().put("type", "clip").put("mime", "text/plain").put("data", text).put("ts", System.currentTimeMillis()).toString()
    if (!send(msg)) pending = text
  }

  companion object { const val MAX_CHARS = 1 shl 20 }
}
