plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

// CI passes the release number (ci.yml); a local build is 0.1.0.
val versionText = System.getenv("UNI_VPN_VERSION") ?: "0.1.0"
val keystore = System.getenv("ANDROID_KEYSTORE")

android {
    namespace = "de.davidvinu.univpn"
    compileSdk = 36

    defaultConfig {
        applicationId = "de.davidvinu.univpn"
        // Android 10: the VPN can carry a proxy rule (VpnService.Builder.setHttpProxy).
        minSdk = 29
        targetSdk = 36
        versionCode = (versionText.substringAfterLast(".").toIntOrNull() ?: 0).coerceAtLeast(1)
        versionName = versionText
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        // The ABIs packaging/android/build-apk.sh builds the core and openconnect for.
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
    }

    signingConfigs {
        // The upload key, from repository secrets (docs/android.md). Without it the release
        // build stays unsigned and Play App Signing or a later step signs it.
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("ANDROID_KEY_ALIAS")
                keyPassword = System.getenv("ANDROID_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            if (keystore != null) signingConfig = signingConfigs.getByName("release")
        }
    }

    // openconnect, ocproxy and the Go core are programs, not libraries: Android only runs
    // programs from the app's native library folder, and only when they are unpacked there.
    packaging {
        jniLibs.useLegacyPackaging = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        compose = true
        buildConfig = true
    }
    // Compose runs in the unit tests (PagesTest): the Android calls it makes (Trace, Log) do
    // nothing there instead of failing.
    testOptions { unitTests.isReturnDefaultValues = true }
    lint {
        abortOnError = true
        warningsAsErrors = false
        disable += setOf("GradleDependency", "NewerVersionAvailable", "AndroidGradlePluginVersion", "OldTargetApi")
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2025.12.01")
    implementation(composeBom)
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")
    implementation("androidx.activity:activity-compose:1.12.2")
    implementation("androidx.core:core-ktx:1.17.0")
    // registerForActivityResult needs at least this fragment version next to Play services.
    implementation("androidx.fragment:fragment-ktx:1.8.9")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.9.4")
    // Google's code scanner: the system scanner opens, no camera permission (role model:
    // Google Authenticator, WhatsApp linked devices).
    implementation("com.google.android.gms:play-services-code-scanner:16.1.0")
    // QR codes for "Add a device".
    implementation("com.google.zxing:core:3.5.4")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517")
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("androidx.test.uiautomator:uiautomator:2.3.0")
}
