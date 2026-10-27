package expo.modules.sharingservice

import android.content.Context
import java.util.UUID

/**
 * Everything the background service needs, persisted so it works with no JS running.
 * The device token is kept as AES-GCM ciphertext under a non-exportable Keystore key.
 */
class Prefs(ctx: Context) {
  private val sp = ctx.applicationContext.getSharedPreferences("sharing", Context.MODE_PRIVATE)

  private val vault = Vault(
    object : KeyValue {
      override fun get(k: String) = sp.getString(k, null)
      override fun put(k: String, v: String) { sp.edit().putString(k, v).apply() }
      override fun remove(k: String) { sp.edit().remove(k).apply() }
    },
    plainKey = "peerToken", sealedKey = "peerTokenSealed", key = DeviceKey::get,
  )

  val deviceId: String
    get() = sp.getString("deviceId", null) ?: UUID.randomUUID().toString().also {
      sp.edit().putString("deviceId", it).apply()
    }

  var peerToken: String?
    get() = vault.get()
    set(v) = vault.set(v)

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

  /** Protocol revision the laptop announced in its last hello (1 = predates resumable transfers). */
  var peerProto: Int
    get() = sp.getInt("peerProto", 2)
    set(v) = sp.edit().putInt("peerProto", v).apply()

  /** User wants the service running (survives reboot). */
  var enabled: Boolean
    get() = sp.getBoolean("enabled", false)
    set(v) = sp.edit().putBoolean("enabled", v).apply()

  /** Opt-in automatic phone-to-laptop clipboard (see ClipWatcher). */
  var autoClipboard: Boolean
    get() = sp.getBoolean("autoClipboard", false)
    set(v) = sp.edit().putBoolean("autoClipboard", v).apply()

  /** Largest share-sheet file copied into the spool so a queued upload survives a restart. */
  var spoolThreshold: Long
    get() = sp.getLong("spoolThreshold", Spool.DEFAULT_THRESHOLD)
    set(v) = sp.edit().putLong("spoolThreshold", v).apply()

  val paired: Boolean get() = peerToken != null && fingerprint != null && laptopId != null

  fun clearPairing() {
    vault.set(null)
    sp.edit().remove("fp").remove("laptopId").remove("laptopName").remove("lastHost").remove("peerProto").apply()
  }
}
