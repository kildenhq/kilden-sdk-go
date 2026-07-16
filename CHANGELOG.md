# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0-alpha.3] - 2026-07-16

### Fixed

- The `Version` constant reports the release it actually ships in. alpha.2 went
  out declaring `0.1.0-alpha.1`, so its User-Agent (spec §4.1) misidentified it
  to ingest and the two were indistinguishable in the logs. That artifact keeps
  the wrong value — tags are immutable and the module proxy has cached it — so
  this is the first release to report itself honestly.
- The release refuses to publish when the tag and `Version` disagree. Cutting a
  release used to mean "update the changelog, push a tag"; the constant was
  never part of the ritual, which is exactly how it drifted.
- A prerelease is no longer published as GitHub's "Latest" release. The flag
  was never set, so alpha.2 was presented as the current release of the SDK.

### Changed

- Repository moved to the kildenhq org; docs follow the move to kilden.io/docs.

## [0.1.0-alpha.2] - 2026-07-14

### Fixed

- Any 2xx from `/capture` is success; the response body is never parsed
  (spec clarification — a 200 with a corrupt body was retried before).

## [0.1.0-alpha.1] - 2026-07-14

### Added

- `Client` with bounded in-memory queue, background delivery, batch
  chunking, gzip, and the spec retry policy (backoff + jitter,
  `Retry-After` on 429).
- `IdentitySigner`: hand-rolled HS256 in the spec's canonical byte form.
- Feature flags via `/decide` with a 30s TTL / 1000-id LRU cache,
  `WithPersonProperties` and `WithDefault`.
- Frozen rollout hashing (internal), pinned by the spec vectors.
- Vector runners and failure-simulation tests against the spec mock server.

[Unreleased]: https://github.com/kildenhq/kilden-sdk-go/compare/v0.1.0-alpha.3...HEAD
[0.1.0-alpha.3]: https://github.com/kildenhq/kilden-sdk-go/compare/v0.1.0-alpha.2...v0.1.0-alpha.3
[0.1.0-alpha.2]: https://github.com/kildenhq/kilden-sdk-go/compare/v0.1.0-alpha.1...v0.1.0-alpha.2
[0.1.0-alpha.1]: https://github.com/kildenhq/kilden-sdk-go/releases/tag/v0.1.0-alpha.1
