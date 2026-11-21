import java.util.Properties

plugins { id("com.android.application"); id("org.jetbrains.kotlin.android") }
val signingFile = rootProject.file("signing/release.properties")
val signing = Properties().apply { if (signingFile.isFile) signingFile.inputStream().use(::load) }
val hasSigning = signingFile.isFile
android {
    namespace = "songya.app"
    compileSdk = 36
    buildToolsVersion = "36.0.0"
    defaultConfig {
        applicationId = "songya.app"
        minSdk = 26
        targetSdk = 36
        versionCode = 26112101
        versionName = "2026.11.21.1"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    signingConfigs {
        if (hasSigning) create("release") {
            storeFile = rootProject.file("signing/release.p12")
            storePassword = signing.getProperty("storePassword")
            keyAlias = "songya"
            keyPassword = signing.getProperty("storePassword")
            enableV1Signing = true; enableV2Signing = true; enableV3Signing = true
        }
    }
    buildTypes {
        release {
            if (hasSigning) signingConfig = signingConfigs.getByName("release")
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
    compileOptions { sourceCompatibility = JavaVersion.VERSION_17; targetCompatibility = JavaVersion.VERSION_17 }
}
kotlin { compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) } }
dependencies {
    implementation("androidx.activity:activity-ktx:1.10.1")
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.9.0")
    implementation("androidx.camera:camera-core:1.6.1")
    implementation("androidx.camera:camera-camera2:1.6.1")
    implementation("androidx.camera:camera-lifecycle:1.6.1")
    implementation("androidx.camera:camera-view:1.6.1")
    implementation("com.google.zxing:core:3.5.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.10.1")
    implementation("com.squareup.okhttp3:okhttp:5.4.0")
    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.uiautomator:uiautomator:2.4.0")
}
val verifyReleaseSigning by tasks.registering {
    doLast { check(hasSigning) { "Build with scripts/build-android.sh to configure private release signing." } }
}
tasks.matching { it.name == "packageRelease" || it.name == "packageReleaseBundle" }.configureEach { dependsOn(verifyReleaseSigning) }
