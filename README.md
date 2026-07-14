<p align="center">
  <img src=".github/assets/hero.png" alt="Kilden Go SDK" width="800">
</p>

# Kilden Go SDK

[![Go Reference](https://pkg.go.dev/badge/go.kilden.io/sdk.svg)](https://pkg.go.dev/go.kilden.io/sdk)
[![ci](https://github.com/freshworkstudio/kilden-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/freshworkstudio/kilden-sdk-go/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/freshworkstudio/kilden-sdk-go)](LICENSE)

[Kilden](https://kilden.io) is a customer data platform — product analytics,
campaigns and session replay on one event pipeline. This is the server-side
Go SDK: batched event capture, identity-token signing and feature flags,
with zero dependencies outside the standard library.

The module lives at `go.kilden.io/sdk` (a vanity import served over the
`kilden.io` domain; the code is hosted in this repository).

```sh
go get go.kilden.io/sdk
```

```go
client, err := kilden.New(os.Getenv("KILDEN_SECRET_KEY"))
if err != nil {
    log.Fatal(err)
}
defer client.Close()

client.Track("user_42", "order_completed", map[string]any{
    "revenue": 99.9, "currency": "CLP",
})
```

Use your project's **secret** write key (`sk_…`), never the public one. The
secret key is what makes server-side events *facts* on the platform
(`source=server`, `verified=true`); a public key in a backend silently
degrades them to browser-grade data, so the constructor rejects it outright.
Keep the secret key out of anything that ships to a client.

## Identity verification

Kilden's trust model stops browser traffic from impersonating your users:
the browser SDK attaches a short-lived JWT that only your backend can sign,
because only your backend knows who is logged in and can hold the identity
secret. `IdentitySigner` makes that signature a three-liner:

```go
signer, err := kilden.NewIdentitySigner(os.Getenv("KILDEN_IDENTITY_SECRET"), "k1")
if err != nil {
    log.Fatal(err)
}

// In the handler that renders your page or serves your session endpoint:
token, err := signer.Sign(user.ID,
    kilden.WithTraits(map[string]any{"plan": user.Plan}),
    kilden.WithTTL(time.Hour),
)
```

`Sign` takes the `distinct_id` the token vouches for (`sub`); the platform
verifies it against each event, byte for byte. Traits signed here override
unsigned traits from the browser during enrichment. Tokens default to a
1-hour TTL and cap at 7 days — no forever-tokens.

**Only sign an id your backend authenticated.** Something like
`signer.Sign(r.URL.Query().Get("user_id"))` lets anyone impersonate anyone —
and stamps the forgery "verified". Sign `session.UserID`, never request
input.

The second half of the story is an endpoint the browser SDK can call to
refresh its token (it does so 60 seconds before expiry and on 401). A plain
`net/http` version:

```go
mux.HandleFunc("POST /kilden/identity", func(w http.ResponseWriter, r *http.Request) {
    user, ok := currentUser(r) // your session auth
    if !ok {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }
    token, err := signer.Sign(user.ID)
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Cache-Control", "no-store") // per-user — never cacheable
    json.NewEncoder(w).Encode(map[string]string{
        "distinct_id": user.ID,
        "token":       token,
    })
})
```

## Events

```go
client.Track("user_42", "invoice_paid", map[string]any{"amount": 12000})

// Set person traits ($identify with a $set body):
client.Identify("user_42", map[string]any{"plan": "pro", "email": u.Email})

// Attach a new identity to an existing person:
client.Alias("anon_0190a1b2-…", "user_42")
```

Every event gets a UUID v7 generated at call time, which makes delivery
retries idempotent server-side. Backfills can pin both the clock and the id:

```go
client.Track("user_42", "imported_order", props,
    kilden.WithTimestamp(orderDate),
    kilden.WithUUID(stableUUIDForRow), // caller-level retries dedupe too
)
```

After `New` succeeds, no method returns an error or panics: invalid input is
dropped and logged through `slog` (the `debug` option adds detail, including
warnings when you use `$`-prefixed names, which belong to Kilden).
`client.DroppedCount()` exposes how many events were lost to a full queue or
exhausted retries.

## Batching and shutdown

Events queue in memory (bounded, default 10 000 — when full, *new* events
drop, never queued ones) and a background goroutine delivers them in batches:
after 20 events or every 10 seconds, whichever comes first. Delivery retries
429s and 5xx with exponential backoff and honors `Retry-After`; other 4xx
are dropped immediately — retrying a 401 is spam.

**You must call `Close()`** (typically `defer client.Close()` in `main`). Go
has no process-exit hook the SDK could register: if main returns with events
still queued, they are gone. `Close` flushes with a 10-second deadline, is
idempotent, and leaves the client inert. `Flush()` is the blocking
mid-process variant.

## Feature flags

```go
if client.IsEnabled("new_checkout", "user_42",
    kilden.WithPersonProperties(map[string]any{"plan": "pro"}),
    kilden.WithDefault(false),
) {
    // …
}

variant := client.FeatureFlag("pricing_experiment", "user_42") // false | true | "variant_b"
```

Flags are evaluated remotely against `/decide` with a 30-second in-process
cache per `distinct_id`. `WithDefault` is what you get when Kilden cannot
answer in time (one attempt, your client `timeout`, no retries — a late flag
answer is useless). `WithPersonProperties` overrides stored person traits
for that evaluation only and bypasses the cache; the option exists now so
local evaluation can land later without an API change.

## Options

```go
kilden.New(key,
    kilden.WithHost("https://ingest.kilden.io"), // self-hosted: your ingest host
    kilden.WithFlushAt(20),
    kilden.WithFlushInterval(10*time.Second),
    kilden.WithMaxQueueSize(10000),
    kilden.WithTimeout(3*time.Second),
    kilden.WithTransport(customRoundTripper),    // proxies, instrumentation
    kilden.WithDebug(false),
    kilden.WithEnabled(false),                   // full no-op: tests, CI
)
```

## Spec

This SDK implements the
[Kilden server SDK specification](https://github.com/freshworkstudio/kilden-sdk-spec)
(spec version 0.1) and runs its frozen test vectors — wire payloads,
byte-exact identity JWTs, flag-rollout hashing — against the spec's mock
server in CI. Behavior changes land in the spec first, then here.

## Community

- [Docs](https://docs.kilden.io) — product documentation.
- [Discussions](https://github.com/freshworkstudio/kilden-sdk-go/discussions)
  — questions and design conversations.

## License

[MIT](LICENSE)
