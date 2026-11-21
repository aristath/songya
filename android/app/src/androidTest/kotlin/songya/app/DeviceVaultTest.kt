package songya.app

import android.content.pm.PackageManager
import android.util.Base64
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.security.KeyFactory
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class DeviceVaultTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    @Test fun keystoreProofSurvivesReopeningAndIsOriginScoped() {
        val origin = "https://identity-fixture.invalid"
        val vault = DeviceVault(context)
        vault.clear()
        val key = vault.publicKey(origin,true)
        vault.save(Credentials(origin))
        val reopened = DeviceVault(context)
        assertEquals(Credentials(origin),reopened.load())
        assertEquals(key,reopened.publicKey(origin,false))
        val message = "songya-web-auth-v1\n" + "A".repeat(43)
        val verifier = Signature.getInstance("SHA256withECDSA")
        verifier.initVerify(KeyFactory.getInstance("EC").generatePublic(X509EncodedKeySpec(Base64.decode(key,Base64.DEFAULT))))
        verifier.update(message.toByteArray(Charsets.UTF_8))
        assertTrue(verifier.verify(Base64.decode(reopened.sign(origin,message),Base64.DEFAULT)))
        assertThrows(IllegalArgumentException::class.java) {reopened.sign(origin,"unrelated signature request")}
        assertThrows(IllegalStateException::class.java) {reopened.publicKey("https://different-origin.invalid",false)}
        reopened.clear()
        assertNull(DeviceVault(context).load())
        assertThrows(IllegalStateException::class.java) {reopened.publicKey(origin,false)}
    }
    @Test fun installedManifestRequestsOnlyWrapperPermissions() {
        @Suppress("DEPRECATION")
        val info = context.packageManager.getPackageInfo(context.packageName,PackageManager.GET_PERMISSIONS or PackageManager.GET_SERVICES or PackageManager.GET_RECEIVERS)
        val androidPermissions = info.requestedPermissions.orEmpty().filter {it.startsWith("android.permission.")}.toSet()
        assertEquals(setOf("android.permission.CAMERA", "android.permission.INTERNET", "android.permission.ACCESS_NETWORK_STATE"), androidPermissions)
        assertTrue(info.services.orEmpty().none {it.exported})
        assertTrue(info.receivers.orEmpty().filter {it.exported}.all {it.permission == "android.permission.DUMP"})
    }
    @Test fun unavailableKeyRetainsPairingMetadataAndOffersRetry() {
        val preferences=context.getSharedPreferences("wrapper",0)
        val origin="https://temporarily-unavailable-fixture.invalid"
        assertTrue(preferences.edit().putString("origin",origin).commit())
        androidx.test.core.app.ActivityScenario.launch(MainActivity::class.java).use { scenario ->
            val device=androidx.test.uiautomator.UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())
            assertTrue(device.wait(androidx.test.uiautomator.Until.hasObject(androidx.test.uiautomator.By.text("Retry connection")),10000))
            assertEquals(origin,preferences.getString("origin",null))
            device.findObject(androidx.test.uiautomator.By.text("Retry connection")).click()
            assertEquals(origin,preferences.getString("origin",null))
            scenario.recreate()
            assertTrue(device.wait(androidx.test.uiautomator.Until.hasObject(androidx.test.uiautomator.By.text("Retry connection")),10000))
            assertEquals(origin,preferences.getString("origin",null))
        }
        assertTrue(preferences.edit().clear().commit())
    }
}
