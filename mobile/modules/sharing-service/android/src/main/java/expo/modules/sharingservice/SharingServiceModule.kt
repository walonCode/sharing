package expo.modules.sharingservice

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import expo.modules.kotlin.Promise
import expo.modules.kotlin.exception.Exceptions
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

private const val PICK_SEND = 7102

/** Thin JS bridge. All real work lives in the service so it runs without JS. */
class SharingServiceModule : Module() {
  private val ctx: Context get() = appContext.reactContext ?: throw Exceptions.ReactContextLost()
  private var pending: Promise? = null
  private val statusListener: () -> Unit = { sendEvent("onStatus", status()) }

  private fun status(): Map<String, Any?> {
    val prefs = Prefs(ctx)
    return mapOf(
      "paired" to prefs.paired,
      "laptopName" to prefs.laptopName,
      "running" to Status.running,
      "connected" to Status.connected,
      "error" to Status.lastError,
    )
  }

  private fun pickTree(code: Int, promise: Promise) {
    val activity = appContext.currentActivity ?: throw Exceptions.MissingActivity()
    pending = promise
    val intent = Intent(Intent.ACTION_OPEN_DOCUMENT_TREE).addFlags(
      Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION or
        Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION or Intent.FLAG_GRANT_PREFIX_URI_PERMISSION,
    )
    activity.startActivityForResult(intent, code)
  }

  override fun definition() = ModuleDefinition {
    Name("SharingService")
    Events("onStatus")

    OnCreate { Status.listeners.add(statusListener) }
    OnDestroy { Status.listeners.remove(statusListener) }

    Function("getStatus") { status() }

    AsyncFunction("pair") { inviteUrl: String, deviceName: String ->
      Net.pair(Prefs(ctx), Invite.parse(inviteUrl), deviceName)
      Status.changed() // tell JS; otherwise the UI keeps showing "not paired"
      status()
    }

    AsyncFunction("unpair") {
      SharingService.stop(ctx)
      Prefs(ctx).apply { Net.shutdown(fingerprint); clearPairing(); enabled = false }
      Status.changed()
      status()
    }

    AsyncFunction("pickFolderToSend") { promise: Promise -> pickTree(PICK_SEND, promise) }

    Function("start") {
      Prefs(ctx).enabled = true
      SharingService.start(ctx)
    }
    Function("stop") {
      Prefs(ctx).enabled = false
      SharingService.stop(ctx)
    }

    // Asks the system to add the "Send clipboard" quick-settings tile (Android 13+); older versions add it by hand.
    AsyncFunction("requestAddClipboardTile") { promise: Promise ->
      if (Build.VERSION.SDK_INT < 33) {
        promise.resolve("unsupported")
        return@AsyncFunction
      }
      val manager = ctx.getSystemService(android.app.StatusBarManager::class.java)
      manager.requestAddTileService(
        android.content.ComponentName(ctx, ClipTileService::class.java),
        "Send clipboard",
        android.graphics.drawable.Icon.createWithResource(ctx, android.R.drawable.ic_menu_send),
        ctx.mainExecutor,
      ) { result -> promise.resolve(result.toString()) }
    }

    // Optional automatic phone-to-laptop clipboard (see ClipWatcher). The service reads the flag at start-up.
    Function("clipboardAccess") {
      mapOf(
        "enabled" to Prefs(ctx).autoClipboard,
        "overlay" to Settings.canDrawOverlays(ctx),
        "logs" to ClipWatcher.hasReadLogs(ctx),
        // False in the default build, which does not declare READ_LOGS or the overlay permission at all.
        "supported" to ClipWatcher.isDeclared(ctx),
        "logActive" to Status.logActive,
      )
    }
    Function("setAutoClipboard") { enabled: Boolean ->
      Prefs(ctx).autoClipboard = enabled
      if (Status.running) {
        SharingService.stop(ctx)
        SharingService.start(ctx)
      }
    }
    Function("requestOverlayPermission") {
      ctx.startActivity(
        Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION, Uri.parse("package:${ctx.packageName}")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
      )
    }

    Function("isBatteryOptimizationIgnored") {
      (ctx.getSystemService(Context.POWER_SERVICE) as PowerManager).isIgnoringBatteryOptimizations(ctx.packageName)
    }
    Function("requestIgnoreBatteryOptimization") {
      val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:${ctx.packageName}"))
        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
      ctx.startActivity(intent)
    }

    OnActivityResult { _, payload ->
      if (payload.requestCode != PICK_SEND) return@OnActivityResult
      val promise = pending ?: return@OnActivityResult
      pending = null
      val uri = payload.data?.data
      if (payload.resultCode != Activity.RESULT_OK || uri == null) {
        promise.resolve(null)
        return@OnActivityResult
      }
      SharingService.send(ctx, SharingService.ACTION_UPLOAD_TREE) {
        data = uri
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
      }
      promise.resolve(uri.toString())
    }
  }
}
