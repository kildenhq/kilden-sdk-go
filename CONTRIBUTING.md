# Contributing

Behavior is governed by
[kilden-sdk-spec](https://github.com/freshworkstudio/kilden-sdk-spec): the
spec and its test vectors are the authority for what this SDK does. A PR
that changes observable behavior without a matching spec change will be
rejected, however good the code — that ordering is what keeps the five
server SDKs identical.

Good targets for PRs here: performance, allocations, error messages, docs,
test coverage, Go-idiom improvements that do not change the wire.

## Running tests

```sh
go test -race ./...
```

The vector runners and integration tests need a checkout of the spec repo;
they look for `../kilden-sdk-spec` (override with `KILDEN_SPEC_DIR`) and
build its mock server with your local Go toolchain. Without the checkout
those tests skip.

## Questions

[Discussions](https://github.com/freshworkstudio/kilden-sdk-go/discussions),
please — answers there stay searchable.
