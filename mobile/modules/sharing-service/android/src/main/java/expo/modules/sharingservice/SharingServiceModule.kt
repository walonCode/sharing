package expo.modules.sharingservice

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import androidx.documentfile.provider.DocumentFile
import expo.modules.kotlin.Promise
import expo.modules.kotlin.exception.Exceptions
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

private const val PICK_RECEIVE = 7101
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
      "folder" to prefs.treeUri?.let { DocumentFile.fromTreeUri(ctx, it)?.name },
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
      status()
    }

    AsyncFunction("unpair") {
      SharingService.stop(ctx)
      Prefs(ctx).apply { clearPairing(); enabled = false }
      status()
    }

    // Laptop folder browsing; each returns the laptop's JSON response as a string.
    AsyncFunction("listLaptopFolders") { path: String ->
      Net.laptop(Prefs(ctx), "GET", "/fs?path=" + java.net.URLEncoder.encode(path, "UTF-8"))
    }
    AsyncFunction("createLaptopFolder") { parent: String, name: String ->
      Net.laptop(Prefs(ctx), "POST", "/fs/mkdir", org.json.JSONObject().put("parent", parent).put("name", name))
    }
    AsyncFunction("setLaptopReceiveFolder") { path: String ->
      Net.laptop(Prefs(ctx), "PUT", "/settings", org.json.JSONObject().put("receiveDir", path))
    }
    AsyncFunction("getLaptopSettings") { Net.laptop(Prefs(ctx), "GET", "/settings") }

    AsyncFunction("pickReceiveFolder") { promise: Promise -> pickTree(PICK_RECEIVE, promise) }
    AsyncFunction("pickFolderToSend") { promise: Promise -> pickTree(PICK_SEND, promise) }

    Function("start") {
      Prefs(ctx).enabled = true
      SharingService.start(ctx)
    }
    Function("stop") {
      Prefs(ctx).enabled = false
      SharingService.stop(ctx)
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
      if (payload.requestCode != PICK_RECEIVE && payload.requestCode != PICK_SEND) return@OnActivityResult
      val promise = pending ?: return@OnActivityResult
      pending = null
      val uri = payload.data?.data
      if (payload.resultCode != Activity.RESULT_OK || uri == null) {
        promise.resolve(null)
        return@OnActivityResult
      }
      val rw = Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION
      ctx.contentResolver.takePersistableUriPermission(uri, rw)
      if (payload.requestCode == PICK_RECEIVE) {
        Prefs(ctx).treeUri = uri
        Status.changed()
      } else {
        SharingService.send(ctx, SharingService.ACTION_UPLOAD_TREE) {
          data = uri
          addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
      }
      promise.resolve(uri.toString())
    }
  }
}
