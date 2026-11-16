pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "hostman-android"

// :core is plain Kotlin — everything the app decides, with no Android in it,
// so its tests run on any JVM. :app is the Android shell around it, and needs
// the Android SDK; -PcoreOnly leaves it out, for a machine without one.
include(":core")
if (!providers.gradleProperty("coreOnly").isPresent) {
    include(":app")
}
