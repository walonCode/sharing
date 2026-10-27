package expo.modules.sharingservice

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Base64
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey

class VaultTest {
  private fun key(): SecretKey = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey()
  private val b64: (ByteArray) -> String = { Base64.getEncoder().encodeToString(it) }
  private val unb64: (String) -> ByteArray = { Base64.getDecoder().decode(it) }

  private class MapKv : KeyValue {
    val m = HashMap<String, String>()
    override fun get(k: String) = m[k]
    override fun put(k: String, v: String) { m[k] = v }
    override fun remove(k: String) { m.remove(k) }
  }

  private fun vault(kv: KeyValue, k: () -> SecretKey) = Vault(kv, "peerToken", "peerTokenSealed", k, b64, unb64)

  @Test fun roundTripAndNoPlaintextAtRest() {
    val k = key(); val kv = MapKv()
    val v = vault(kv, { k })
    v.set("s3cret-token")
    assertEquals("s3cret-token", v.get())
    assertFalse(kv.m.values.any { it.contains("s3cret") })
    assertNull(kv.m["peerToken"])
  }

  @Test fun sealingIsRandomised() {
    val k = key()
    assertNotEquals(SecretBox.seal(k, "x", b64), SecretBox.seal(k, "x", b64))
  }

  @Test fun wrongKeyOrTamperedDataFailClosed() {
    val sealed = SecretBox.seal(key(), "x", b64)
    assertNull(SecretBox.open(key(), sealed, unb64))
    val k = key()
    val good = SecretBox.seal(k, "x", b64)
    val raw = unb64(good); raw[raw.size - 1] = (raw[raw.size - 1].toInt() xor 1).toByte()
    assertNull(SecretBox.open(k, b64(raw), unb64))
    assertNull(SecretBox.open(k, "not base64 !!", unb64))
  }

  @Test fun plaintextFromAnOlderVersionIsMigratedOnFirstRead() {
    val k = key(); val kv = MapKv()
    kv.put("peerToken", "old-plain-token")
    val v = vault(kv, { k })
    assertEquals("old-plain-token", v.get())
    assertNull("plaintext must be erased after migration", kv.m["peerToken"])
    assertTrue(kv.m.containsKey("peerTokenSealed"))
    assertEquals("old-plain-token", vault(kv, { k }).get())
  }

  @Test fun lostKeyMeansUnpairedNotACrash() {
    val kv = MapKv()
    vault(kv, { key() }).set("tok")
    assertNull(vault(kv, { key() }).get()) // key replaced (restore on another device, reinstall)
  }

  @Test fun brokenKeystoreFallsBackToPlainStorage() {
    val kv = MapKv()
    val v = vault(kv, { throw IllegalStateException("keystore unavailable") })
    v.set("tok")
    assertTrue(v.usingFallback)
    assertEquals("tok", v.get())
  }

  @Test fun clearingRemovesBothForms() {
    val k = key(); val kv = MapKv()
    val v = vault(kv, { k }); v.set("tok"); v.set(null)
    assertNull(v.get()); assertTrue(kv.m.isEmpty())
  }
}
