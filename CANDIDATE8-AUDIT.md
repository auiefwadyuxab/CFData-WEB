# Candidate 8 audit

- Based on Candidate 7.
- Restored the missing `gradle/wrapper/gradle-wrapper.jar`.
- Wrapper JAR SHA-256: `497c8c2a7e5031f6aa847f88104aa80a93532ec32ee17bdb8d1d2f67a194a9c7`, matching the official Gradle 9.6.1 Wrapper JAR checksum.
- Changed the wrapper distribution from Gradle 9.7.0 to Gradle 9.6.1 so the checked-in wrapper JAR and declared distribution are consistent.
- AGP 9.3 requires Gradle 9.5.0 or newer, so 9.6.1 is within the supported range.
- No application source changes beyond Candidate 7.
