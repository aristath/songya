package songya.app

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.Signature
import java.security.spec.ECGenParameterSpec

/** The long-lived signing credential is non-exportable. Only the public server origin is stored as metadata. */
class DeviceVault(context: Context) {
    private val preferences = context.getSharedPreferences("wrapper", Context.MODE_PRIVATE)
    private val store: KeyStore get() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    fun load(): Credentials? {
        val origin = preferences.getString("origin", null) ?: return null
        require(normalizeOrigin(java.net.URI(origin)) == origin && store.containsAlias(alias(origin))) { "Pair this device again" }
        return Credentials(origin)
    }
    fun save(credentials: Credentials) {
        check(preferences.edit().putString("origin", credentials.origin).commit())
    }
    fun publicKey(origin: String, create: Boolean): String {
        val identity = alias(origin)
        if (!store.containsAlias(identity)) {
            check(create) { "Pair this device again" }
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(KeyGenParameterSpec.Builder(identity, KeyProperties.PURPOSE_SIGN)
                    .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                    .setDigests(KeyProperties.DIGEST_SHA256).build())
            }.generateKeyPair()
        }
        return Base64.encodeToString(store.getCertificate(identity).publicKey.encoded, Base64.NO_WRAP)
    }
    fun sign(origin: String, message: String): String {
        val identity = alias(origin)
        require(message.matches(Regex("songya-web-auth-v1\\n[A-Za-z0-9_-]{43}")))
        val signer = Signature.getInstance("SHA256withECDSA")
        signer.initSign(store.getKey(identity, null) as java.security.PrivateKey)
        signer.update(message.toByteArray(Charsets.UTF_8))
        return Base64.encodeToString(signer.sign(), Base64.NO_WRAP)
    }
    fun clear() {
        val origin = preferences.getString("origin", null)
        check(preferences.edit().clear().commit())
        if (origin != null && store.containsAlias(alias(origin))) store.deleteEntry(alias(origin))
    }
    private fun alias(origin: String): String = "songya.wrapper." + java.security.MessageDigest.getInstance("SHA-256").digest(origin.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it) }
}
