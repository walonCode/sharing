package expo.modules.sharingservice

import android.content.ClipboardManager
import android.content.Context
import android.content.pm.PackageManager
import android.graphics.PixelFormat
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import android.view.View
import android.view.WindowManager
import java.io.BufferedReader
import java.io.InputStreamReader

/**
 * Optional automatic phone-to-laptop clipboard. Off unless the user enables it in the app.
 *
 * Android 10+ hides the clipboard from background apps: change callbacks are not
 * delivered and reads return nothing. This works around that with two permissions
 * the user grants on purpose:
 *  1. Detection: READ_LOGS (granted once over adb). When another app changes the
 *     clipboard, the system logs a line naming this package, which we watch for.
 *  2. Reading: "Display over other apps". A 1px focusable overlay window gives the
 *     app focus for a moment, which is the condition Android requires to read the
 *     clipboard.
 * It is fragile (it depends on system log wording) and each read may show Android's
 * own "pasted from clipboard" toast. The notification button and tile remain the
 * reliable fallback.
 */
class ClipWatcher(private val ctx: Context, private val onText: (String) -> Unit) {
  private val main = Handler(Looper.getMainLooper())
  private val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
  private val wm = ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager
  @Volatile private var logcat: Process? = null
  @Volatile private var stopped = false
  private var pending = false

  private val listener = ClipboardManager.OnPrimaryClipChangedListener {
    Log.d(TAG, "clip listener fired")
    changed()
  }

  fun start() {
    stopped = false
    try {
      cm.addPrimaryClipChangedListener(listener)
    } catch (e: Exception) {
      Log.w(TAG, "listener unavailable: $e")
    }
    if (hasReadLogs(ctx)) {
      Thread(::watchLog, "sharing-clip-log").also { it.isDaemon = true; it.start() }
    } else {
      Log.i(TAG, "READ_LOGS not granted; only the change listener is active")
    }
  }

  fun stop() {
    stopped = true
    try { cm.removePrimaryClipChangedListener(listener) } catch (_: Exception) {}
    logcat?.destroy()
  }

  /** Debounced: one copy can log several lines. */
  private fun changed() {
    main.post {
      if (pending) return@post
      pending = true
      main.postDelayed({ pending = false; read() }, 150)
    }
  }

  private fun watchLog() {
    try {
      // -T 1: only lines logged from now on. Filtering happens here so a different tag on other builds still matches.
      val p = ProcessBuilder("logcat", "-T", "1", "-v", "brief").redirectErrorStream(true).start()
      logcat = p
      BufferedReader(InputStreamReader(p.inputStream)).use { r ->
        while (!stopped) {
          val line = r.readLine() ?: break
          if (line.contains("lipboard", ignoreCase = true) && line.contains(ctx.packageName) && !line.contains(TAG)) {
            Log.d(TAG, "log trigger: ${line.take(160)}")
            changed()
          }
        }
      }
    } catch (e: Exception) {
      Log.w(TAG, "log watcher stopped: $e")
    }
  }

  private fun readDirect(): String? = try {
    cm.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(ctx)?.toString()
  } catch (e: SecurityException) {
    null
  }

  private fun read() {
    readDirect()?.takeIf { it.isNotEmpty() }?.let {
      Log.d(TAG, "read directly (${it.length} chars)")
      onText(it)
      return
    }
    if (!Settings.canDrawOverlays(ctx)) {
      Log.i(TAG, "no overlay permission; cannot read the clipboard in the background")
      return
    }
    readViaOverlay()
  }

  private fun readViaOverlay() {
    var done = false
    lateinit var view: View
    fun finish() {
      if (done) return
      done = true
      try { wm.removeViewImmediate(view) } catch (_: Exception) {}
    }
    view = object : View(ctx) {
      override fun onWindowFocusChanged(hasWindowFocus: Boolean) {
        super.onWindowFocusChanged(hasWindowFocus)
        if (!hasWindowFocus || done) return
        val text = readDirect()
        Log.d(TAG, "overlay focused, clip length=${text?.length}")
        finish()
        if (!text.isNullOrEmpty()) onText(text)
      }
    }
    val type = if (Build.VERSION.SDK_INT >= 26) {
      WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY
    } else {
      @Suppress("DEPRECATION") WindowManager.LayoutParams.TYPE_PHONE
    }
    // Focusable (no FLAG_NOT_FOCUSABLE) so it can take focus, but it ignores touches.
    val params = WindowManager.LayoutParams(
      1, 1, type,
      WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE or WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS,
      PixelFormat.TRANSLUCENT,
    )
    try {
      wm.addView(view, params)
      main.postDelayed({
        if (!done) { Log.d(TAG, "overlay never got focus"); finish() }
      }, 1500)
    } catch (e: Exception) {
      Log.w(TAG, "overlay failed: $e")
    }
  }

  companion object {
    const val TAG = "SharingClip"

    fun hasReadLogs(ctx: Context) =
      ctx.checkSelfPermission("android.permission.READ_LOGS") == PackageManager.PERMISSION_GRANTED
  }
}
