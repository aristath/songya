# Songya for Android

A thin wrapper around Songya's web app, adapted from the Clara/Life Log shell.
Memories, editing, chat and settings stay on the server. Android handles QR
scanning, its Keystore identity, WebView, file selection and downloads.

## Connect a phone

1. Configure `public_url` as the server's public HTTPS origin and route that
   hostname to Songya through a tunnel or reverse proxy.
2. Open the desired store in the browser and select **Connect phone**.
3. Download and install **Songya.apk**, then select **Create pairing code**.
4. Scan the code with Songya for Android and confirm the server address.

The code expires after five minutes and is single-use. A paired phone can use
only the selected store, including its memories, files, chat and settings. It
cannot list or access other stores. The existing administrator password still
opens all configured stores in the browser; Android never stores that password.

The phone keeps a non-exportable P-256 signing key in Android Keystore and renews
web access through signed one-use challenges and short-lived browser handoffs.
Network failures preserve pairing and offer retry. **Revoke access** in Connect,
or **Log out** in the app, invalidates that device's sessions and pending proofs.
Changing the administrator password or remapping the selected store requires
new pairing. Re-pairing a key cannot revive its previous browser cookies.

The app works through mobile data or other Wi-Fi networks while the Songya server
and its public HTTPS endpoint are online. It does not add offline editing or push
notifications. Files and media use the existing web APIs and Android's pickers.

## Build and publish

Use JDK 21, Android SDK 36 and build tools 36.0.0:

```sh
JAVA_HOME=/path/to/jdk21 ANDROID_HOME=/path/to/android-sdk \
  scripts/build-android.sh :app:testDebugUnitTest :app:lintRelease
```

This creates `android/Songya.apk`, signed as `songya.app`. The first build creates
private signing material under the ignored `android/signing/` directory. Keep
those files securely outside Git; future updates must use the same signing key.
APKs, private keys and Gradle outputs are not committed.

Set `android_apk` in the private server configuration to the installed release's
absolute path. The **Download Android app** link serves it only to authenticated
browsers and paired phones. The executable embeds the web assets but does not
embed an APK or signing material.

Device public keys and hashed authorization tokens are stored in the private
`mobile_state` JSON file, separate from the memory stores. By default it is named
`<configuration-name>.mobile.json` beside the administrator configuration.
Completed authorization writes are synced before acknowledgement. Interrupted
publications recover on the next access; damaged authorizations preserve their
bytes and do not block administrator access to healthy memories.
