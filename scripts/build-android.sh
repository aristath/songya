#!/usr/bin/env bash
# Build a signed release. Keep android/signing/ to sign future updates with the same key.
set -euo pipefail
app_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$app_dir/android"
umask 077
mkdir -p signing
if [[ ! -f signing/release.properties ]]; then
    if [[ -e signing/release.p12 ]]; then
        echo "Restore signing/release.properties before rebuilding; the existing signing key is preserved." >&2
        exit 1
    fi
    signing_password="$(python3 -c 'import secrets; print(secrets.token_urlsafe(36))')"
    printf 'storePassword=%s\n' "$signing_password" > signing/release.properties
    keytool -genkeypair -keystore signing/release.p12 -storetype PKCS12 \
        -storepass:file <(printf '%s' "$signing_password") -alias songya -keyalg RSA \
        -keysize 3072 -validity 10000 -dname 'CN=Songya Android' >/dev/null
    unset signing_password
fi
./gradlew "$@" :app:assembleRelease
cp app/build/outputs/apk/release/app-release.apk Songya.apk
printf 'Signed APK: %s/android/Songya.apk\n' "$app_dir"
