# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/freshworkstudio/kilden-sdk-go/compare/v0.1.0-alpha.1...HEAD
[0.1.0-alpha.1]: https://github.com/freshworkstudio/kilden-sdk-go/releases/tag/v0.1.0-alpha.1
