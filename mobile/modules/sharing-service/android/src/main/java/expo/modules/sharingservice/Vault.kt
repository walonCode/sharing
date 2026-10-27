package expo.modules.sharingservice

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** AES-GCM envelope: base64(iv || ciphertext || tag). Pure JCA so it can be unit tested on a JVM. */
object SecretBox {
  private const val IV_BYTES = 12
  private const val TAG_BITS = 128

  fun seal(key: SecretKey, plain: String, b64: (ByteArray) -> String = ::androidB64): String {
    val iv = ByteArray(IV_BYTES).also { SecureRandom().nextBytes(it) }
    val c = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key, GCMParameterSpec(TAG_BITS, iv)) }
    return b64(iv + c.doFinal(plain.toByteArray(Charsets.UTF_8)))
  }

  /** Returns null if the data is malformed or was sealed with another key. */
  fun open(key: SecretKey, sealed: String, unb64: (String) -> ByteArray = ::androidUnB64): String? = try {
    val raw = unb64(sealed)
    val c = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(TAG_BITS, raw.copyOfRange(0, IV_BYTES))) }
    String(c.doFinal(raw, IV_BYTES, raw.size - IV_BYTES), Charsets.UTF_8)
  } catch (e: Exception) {
    null
  }

  private fun androidB64(b: ByteArray) = Base64.encodeToString(b, Base64.NO_WRAP)
  private fun androidUnB64(s: String) = Base64.decode(s, Base64.NO_WRAP)
}

/** The device's non-exportable AES key in the Android Keystore. */
object DeviceKey {
  private const val ALIAS = "sharing-token-key"

  fun get(): SecretKey {
    val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
    val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
    gen.init(
      KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
        .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
        .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
        .setKeySize(256)
        .build(),
    )
    return gen.generateKey()
  }

  fun delete() {
    runCatching { KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry(ALIAS) }
  }
}

/** Minimal key-value surface so the vault logic is testable without SharedPreferences. */
interface KeyValue {
  fun get(k: String): String?
  fun put(k: String, v: String)
  fun remove(k: String)
}

/**
 * Stores one secret as ciphertext under [sealedKey]. A plaintext value left by an
 * older version under [plainKey] is encrypted and erased on first read. If the
 * keystore is unusable (broken on a few devices) the secret falls back to plain
 * storage rather than making the app unusable; [usingFallback] reports it.
 */
class Vault(
  private val kv: KeyValue,
  private val plainKey: String,
  private val sealedKey: String,
  private val key: () -> SecretKey,
  private val b64: (ByteArray) -> String = { Base64.encodeToString(it, Base64.NO_WRAP) },
  private val unb64: (String) -> ByteArray = { Base64.decode(it, Base64.NO_WRAP) },
) {
  var usingFallback = false
    private set

  fun get(): String? {
    kv.get(sealedKey)?.let { sealed ->
      val k = runCatching(key).getOrNull()
      SecretBox.open(k ?: return null, sealed, unb64)?.let { return it }
    }
    val plain = kv.get(plainKey) ?: return null
    set(plain) // migrate: encrypt, then erase the plaintext
    return plain
  }

  fun set(value: String?) {
    if (value == null) {
      kv.remove(sealedKey)
      kv.remove(plainKey)
      return
    }
    val k = runCatching(key).getOrNull()
    if (k == null) {
      usingFallback = true
      kv.put(plainKey, value)
      return
    }
    kv.put(sealedKey, SecretBox.seal(k, value, b64))
    kv.remove(plainKey)
  }
}
