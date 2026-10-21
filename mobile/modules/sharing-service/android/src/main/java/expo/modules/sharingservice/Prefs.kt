package expo.modules.sharingservice

import android.content.Context
import java.util.UUID

/** Everything the background service needs, persisted so it works with no JS running. */
class Prefs(ctx: Context) {
  private val sp = ctx.applicationContext.getSharedPreferences("sharing", Context.MODE_PRIVATE)

  val deviceId: String
    get() = sp.getString("deviceId", null) ?: UUID.randomUUID().toString().also {
      sp.edit().putString("deviceId", it).apply()
    }

  var peerToken: String?
    get() = sp.getString("peerToken", null)
    set(v) = sp.edit().putString("peerToken", v).apply()

  var fingerprint: String?
    get() = sp.getString("fp", null)
    set(v) = sp.edit().putString("fp", v).apply()

  /** Laptop's device id; also its mDNS instance name. */
  var laptopId: String?
    get() = sp.getString("laptopId", null)
    set(v) = sp.edit().putString("laptopId", v).apply()

  var laptopName: String?
    get() = sp.getString("laptopName", null)
    set(v) = sp.edit().putString("laptopName", v).apply()

  /** Last address that worked; tried before falling back to mDNS. */
  var lastHost: String?
    get() = sp.getString("lastHost", null)
    set(v) = sp.edit().putString("lastHost", v).apply()

  var lastPort: Int
    get() = sp.getInt("lastPort", 53317)
    set(v) = sp.edit().putInt("lastPort", v).apply()

  /** User wants the service running (survives reboot). */
  var enabled: Boolean
    get() = sp.getBoolean("enabled", false)
    set(v) = sp.edit().putBoolean("enabled", v).apply()

  /** Opt-in automatic phone-to-laptop clipboard (see ClipWatcher). */
  var autoClipboard: Boolean
    get() = sp.getBoolean("autoClipboard", false)
    set(v) = sp.edit().putBoolean("autoClipboard", v).apply()

  val paired: Boolean get() = peerToken != null && fingerprint != null && laptopId != null

  fun clearPairing() {
    sp.edit().remove("peerToken").remove("fp").remove("laptopId").remove("laptopName").remove("lastHost").apply()
  }
}
