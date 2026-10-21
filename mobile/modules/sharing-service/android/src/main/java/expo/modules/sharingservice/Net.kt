package expo.modules.sharingservice

import android.net.Uri
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

class Invite(val host: String, val port: Int, val fingerprint: String, val token: String, val name: String) {
  companion object {
    fun parse(url: String): Invite {
      val u = Uri.parse(url.trim())
      require(u.scheme == "sharing" && u.host == "pair") { "Not a sharing pairing link" }
      fun q(k: String) = u.getQueryParameter(k)?.takeIf { it.isNotEmpty() } ?: throw IllegalArgumentException("Pairing link is missing $k")
      return Invite(q("host"), q("port").toInt(), q("fp").lowercase(), q("token"), u.getQueryParameter("name") ?: "laptop")
    }
  }
}

object Net {
  /** Trusts exactly one certificate: the one whose SHA-256 matches the pairing QR. */
  private class PinnedTrust(private val fingerprint: String) : X509TrustManager {
    override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) = throw CertificateException("client certs unsupported")
    override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {
      val got = MessageDigest.getInstance("SHA-256").digest(chain[0].encoded).joinToString("") { "%02x".format(it) }
      if (got != fingerprint) throw CertificateException("Certificate does not match the paired laptop")
    }
    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
  }

  /** Hostname checks are skipped on purpose: the laptop's IP changes, the pinned key does not. */
  fun client(fingerprint: String): OkHttpClient {
    val trust = PinnedTrust(fingerprint)
    val ctx = SSLContext.getInstance("TLS").apply { init(null, arrayOf(trust), null) }
    return OkHttpClient.Builder()
      .sslSocketFactory(ctx.socketFactory, trust)
      .hostnameVerifier { _, _ -> true }
      .connectTimeout(5, TimeUnit.SECONDS)
      .readTimeout(0, TimeUnit.SECONDS)
      .writeTimeout(0, TimeUnit.SECONDS)
      .pingInterval(20, TimeUnit.SECONDS)
      .build()
  }

  /** Redeems the invite and stores the long-term credentials. Blocking. */
  fun pair(prefs: Prefs, invite: Invite, deviceName: String) {
    val body = JSONObject()
      .put("token", invite.token)
      .put("deviceId", prefs.deviceId)
      .put("name", deviceName)
      .toString()
      .toRequestBody("application/json".toMediaType())
    val req = Request.Builder().url("https://${invite.host}:${invite.port}/pair").post(body).build()
    client(invite.fingerprint).newCall(req).execute().use { resp ->
      val text = resp.body?.string().orEmpty()
      if (!resp.isSuccessful) error(if (resp.code == 403) "Pairing link expired or already used. Run `sharingd pair` again." else "Pairing failed (${resp.code})")
      val json = JSONObject(text)
      prefs.peerToken = json.getString("peerToken")
      prefs.fingerprint = invite.fingerprint
      prefs.laptopId = json.getString("deviceId")
      prefs.laptopName = json.optString("laptopName", invite.name)
      prefs.lastHost = invite.host
      prefs.lastPort = invite.port
    }
  }
}
