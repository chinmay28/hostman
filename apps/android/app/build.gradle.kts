plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// The HostMan that runs on the phone in full-stack mode is the real hostman
// binary, built from this repository's server/ by the Go toolchain (with the
// PWA embedded) and packaged as a native library (libhostman.so), which is
// the one place Android lets an app keep a file it may execute.
//
//   -PappServer=false     build a thin client only, without Go or the web build
//   -PappGo=/path/to/go   the go command, if it is not on PATH
//   -PappVersionName=v2026.11.82 -PappVersionCode=82 -PappVersionDate=2026-11
//                         stamped by the release workflow (scripts/version.mjs)
val repoRoot = rootProject.projectDir.resolve("../..").canonicalFile
val withServer = (findProperty("appServer") as String?) != "false"
val goCommand = (findProperty("appGo") as String?) ?: "go"
val serverLibs = layout.buildDirectory.dir("generated/hostmanServer/jniLibs")

android {
    namespace = "io.github.chinmay28.hostman"
    compileSdk = 35

    defaultConfig {
        applicationId = "io.github.chinmay28.hostman"
        // Android 10: scoped storage and MediaStore.Downloads, so saving a
        // file needs no storage permission.
        minSdk = 29
        targetSdk = 35
        // Stamped by the release workflow: the release's version as the
        // name, its commit count (which only grows) as the code.
        versionCode = (findProperty("appVersionCode") as String?)?.toInt() ?: 1
        versionName = (findProperty("appVersionName") as String?) ?: "dev"
        buildConfigField("boolean", "HAS_SERVER", withServer.toString())
    }

    buildFeatures {
        buildConfig = true
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // Signed with the release key when the build is handed one (the
            // release workflow decodes it from the ANDROID_KEYSTORE_* secrets),
            // so every release installs over the last. Without one, the debug
            // key: it sideloads, but it differs from machine to machine, so an
            // APK signed with it never installs over one built elsewhere.
            signingConfig = System.getenv("ANDROID_KEYSTORE")?.takeIf { it.isNotEmpty() }?.let { path ->
                signingConfigs.create("release") {
                    storeFile = file(path)
                    storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
                    keyAlias = System.getenv("ANDROID_KEY_ALIAS")?.takeIf { it.isNotEmpty() } ?: "harbor"
                    keyPassword = System.getenv("ANDROID_KEY_PASSWORD")?.takeIf { it.isNotEmpty() }
                        ?: System.getenv("ANDROID_KEYSTORE_PASSWORD")
                }
            } ?: signingConfigs.getByName("debug")
        }
    }

    sourceSets["main"].jniLibs.srcDir(serverLibs)

    packaging {
        jniLibs {
            // Installed as a file on disk, not mapped from inside the APK:
            // the server is exec'd, not loaded.
            useLegacyPackaging = true
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    lint {
        disable += setOf(
            // Plain HTTP is how HostMan runs on a home network and on
            // 127.0.0.1, and a private CA is how it runs behind TLS at home:
            // both are the point (see network_security_config.xml).
            "InsecureBaseConfiguration",
            "AcceptsUserCertificates",
            // allowBackup is off; there is nothing for fullBackupContent to say.
            "DataExtractionRules",
            // Versions move on purpose, together, not whenever lint notices.
            "GradleDependency",
        )
    }
}

if (withServer) {
    val version = (findProperty("appVersionCode") as String?) ?: "0"
    val date = (findProperty("appVersionDate") as String?) ?: ""
    val versionPkg = "github.com/chinmay28/hostman/server/internal/version"
    val webDist = repoRoot.resolve("server/internal/web/dist")

    // The binary embeds the PWA, so the PWA is built first, straight into the
    // server's embed directory, exactly as `make build` does.
    val buildWeb = tasks.register<Exec>("buildHostmanWeb") {
        description = "Builds the PWA into server/internal/web/dist"
        workingDir = repoRoot.resolve("apps/web")
        inputs.files(fileTree(repoRoot.resolve("apps/web")) {
            exclude("node_modules/**", "dist/**", "**/*.test.*")
        })
        inputs.file(repoRoot.resolve("scripts/version.mjs"))
        outputs.dir(webDist)
        commandLine("sh", "-c", "[ -d node_modules ] || npm ci --no-audit --no-fund; npm run build")
        doLast {
            // A bundle without its page would still compile into a binary
            // that serves "not found": fail here instead.
            if (!webDist.resolve("index.html").isFile || !webDist.resolve("assets").isDirectory) {
                throw GradleException("the web build left no PWA in $webDist")
            }
        }
    }

    // A static linux/arm64 binary, CGO off: the pure-Go SQLite's C runtime
    // needs a static linux binary on Android (see docs/android.md), and Go's
    // linux port reads no zone database there, so it carries its own
    // (timetzdata). DNS and the phone's zone come from the app; see
    // server/internal/hostenv.
    val out = serverLibs.map { it.file("arm64-v8a/libhostman.so") }
    val buildServer = tasks.register<Exec>("buildHostmanServer") {
        description = "Builds hostman for arm64-v8a as libhostman.so"
        dependsOn(buildWeb)
        workingDir = repoRoot.resolve("server")
        inputs.files(fileTree(repoRoot.resolve("server")) {
            include("go.mod", "go.sum", "cmd/**/*.go", "internal/**")
            exclude("**/*_test.go", "**/testdata/**")
        })
        inputs.property("version", version)
        inputs.property("date", date)
        outputs.file(out)
        environment("CGO_ENABLED", "0")
        environment("GOOS", "linux")
        environment("GOARCH", "arm64")
        commandLine(
            goCommand, "build", "-trimpath", "-tags", "timetzdata",
            "-ldflags=-s -w -X $versionPkg.Patch=$version -X $versionPkg.Date=$date",
            "-o", out.get().asFile.absolutePath,
            "./cmd/hostman",
        )
    }
    tasks.named("preBuild") { dependsOn(buildServer) }
}

dependencies {
    implementation(project(":core"))
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.activity:activity-ktx:1.9.3")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.preference:preference-ktx:1.2.1")
    implementation("androidx.webkit:webkit:1.12.1")
    implementation("com.google.android.material:material:1.12.0")
}
