package expo.modules.sharingservice

import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

class UriPolicyTest {
  private val authorities = setOf("com.walon.sharing", "com.walon.sharing.fileprovider")
  private val dirs = listOf("/data/user/0/com.walon.sharing", "/storage/emulated/0/Android/data/com.walon.sharing")

  private fun reject(u: String) = UriPolicy.reject(u, authorities, dirs)

  @Test fun acceptsOrdinaryProviders() {
    assertNull(reject("content://media/external/images/media/42"))
    assertNull(reject("content://com.android.providers.downloads.documents/document/17"))
    assertNull(reject("content://com.google.android.apps.photos.contentprovider/-1/1/content%3A%2F%2Fmedia%2F1/ORIGINAL/NONE/1"))
  }

  @Test fun rejectsFileAndOtherSchemes() {
    for (u in listOf("file:///sdcard/a.txt", "file:///data/user/0/com.walon.sharing/shared_prefs/sharing.xml", "http://x/y", "android.resource://x/1", "", "no-scheme"))
      assertNotNull(u, reject(u))
  }

  @Test fun rejectsTheAppsOwnProviders() {
    assertNotNull(reject("content://com.walon.sharing.fileprovider/root/shared_prefs/sharing.xml"))
    assertNotNull(reject("content://COM.WALON.SHARING/anything"))
  }

  @Test fun rejectsPathsIntoPrivateStorageEvenWhenEncoded() {
    assertNotNull(reject("content://some.provider/path/data/user/0/com.walon.sharing/shared_prefs/x.xml"))
    assertNotNull(reject("content://some.provider/%2Fdata%2Fuser%2F0%2Fcom.walon.sharing%2Ffiles%2Fx"))
  }

  @Test fun userInfoCannotHideTheAuthority() {
    assertNotNull(reject("content://harmless@com.walon.sharing.fileprovider/x"))
  }
}
