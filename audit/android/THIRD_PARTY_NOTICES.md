# MDD Android preview distribution notices

Application source: https://github.com/lovitus/mdd-sim-gateway/tree/c639dd2b77007a023e2ce44214f1a79503bb5dc3/android-agent

MDD Sim Gateway contributors, 2026. The MDD application source is provided under the repository's GPL-3.0-only license; the complete GPL text is included as LICENSE. The exact Android source and build definitions accompany this preview as android-agent-source.tar.gz. Dependencies retain their own licenses and copyrights. This notice does not relicense them.

The APK includes the following runtime dependency families. See runtime-dependencies.txt for the exact Gradle-resolved graph:

- OkHttp 4.12.0 and Okio: Square, Inc. and contributors; Apache License 2.0. Source: https://github.com/square/okhttp and https://github.com/square/okio
- Kotlin standard library: JetBrains s.r.o. and Kotlin contributors; Apache License 2.0. Source: https://github.com/JetBrains/kotlin
- ZXing Android Embedded 4.3.0: Journey Mobile, Barend Engelbrecht and contributors; Apache License 2.0. Source: https://github.com/journeyapps/zxing-android-embedded
- ZXing core: ZXing authors and contributors; Apache License 2.0. Source: https://github.com/zxing/zxing
- AndroidX libraries: The Android Open Source Project and contributors; Apache License 2.0. Source: https://android.googlesource.com/platform/frameworks/support/
- JetBrains annotations: JetBrains s.r.o.; Apache License 2.0. Source: https://github.com/JetBrains/java-annotations
- OkHttp public suffix data: its original packaged NOTICE is copied unchanged to publicsuffix-NOTICE. It retains the notice and source location for Mozilla Public Suffix List data, separately from the application's license.

The full Apache License 2.0 text accompanies this distribution as APACHE-2.0.txt. The project lineage notice and existing third-party attributions are also included as REPOSITORY_NOTICE and REPOSITORY_THIRD_PARTY_LICENSES.md; those cover the whole repository and do not imply that the native APK embeds its Go Provider or every repository dependency.

JUnit, MockWebServer, okhttp-tls and AndroidX test libraries are used in tests, not the release application's runtime dependency configuration. The APK is signed with a disposable preview key, not an owner production certificate. No private signing material accompanies it. Preserve these notices when redistributing the preview.
