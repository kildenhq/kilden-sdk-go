package kilden

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	defaultTTL = time.Hour
	maxTTL     = 7 * 24 * time.Hour
)

// IdentitySigner signs the short-lived identity tokens Kilden's trust model
// verifies (SPEC.md §6). It is deliberately separate from Client — a
// page-rendering handler wants a token, not an event queue:
//
//	signer, err := kilden.NewIdentitySigner(os.Getenv("KILDEN_IDENTITY_SECRET"), "k1")
//	token, err := signer.Sign(user.ID, kilden.WithTraits(map[string]any{"plan": user.Plan}))
//
// Only sign a sub your backend authenticated. Signing a caller-supplied id
// lets anyone impersonate anyone — with a "verified" stamp on top.
type IdentitySigner struct {
	secret []byte
	kid    string
	now    func() time.Time
}

// SignOption customizes one Sign call.
type SignOption func(*signOpts)

type signOpts struct {
	ttl    time.Duration
	traits map[string]any
}

// WithTTL overrides the token lifetime (default 1h, max 7 days — no
// infinite tokens by design).
func WithTTL(d time.Duration) SignOption {
	return func(o *signOpts) { o.ttl = d }
}

// WithTraits embeds signed traits: they override unsigned traits of the same
// event during enrichment.
func WithTraits(traits map[string]any) SignOption {
	return func(o *signOpts) { o.traits = traits }
}

// NewIdentitySigner builds a signer for one identity secret. kid is
// required: the platform looks the secret up by kid, and a token with an
// unknown kid fails verification silently.
func NewIdentitySigner(identitySecret, kid string) (*IdentitySigner, error) {
	if identitySecret == "" {
		return nil, errors.New("kilden: identity secret is required")
	}
	if kid == "" {
		return nil, errors.New("kilden: kid is required — the platform resolves the secret by kid")
	}
	return &IdentitySigner{secret: []byte(identitySecret), kid: kid, now: time.Now}, nil
}

// Sign returns the canonical HS256 JWT for sub (SPEC.md §6.1). Unlike the
// event hot path this returns errors: signing happens where the caller can
// handle them, and a silently wrong token would fail verification silently.
func (s *IdentitySigner) Sign(sub string, opts ...SignOption) (string, error) {
	if sub == "" {
		return "", errors.New("kilden: sub is required — it must equal the distinct_id of the events it vouches for")
	}
	o := signOpts{ttl: defaultTTL}
	for _, opt := range opts {
		opt(&o)
	}
	if o.ttl <= 0 || o.ttl > maxTTL {
		return "", fmt.Errorf("kilden: ttl must be in (0, %s], got %s", maxTTL, o.ttl)
	}

	iat := s.now().Unix()
	return s.signAt(sub, iat, iat+int64(o.ttl.Seconds()), o.traits)
}

// signAt is the deterministic core, split out so the identity vectors can
// pin iat/exp. json.Marshal of maps produces exactly the canonical form the
// spec freezes: keys sorted lexicographically at every level, compact
// separators, &, <, > escaped, UTF-8 preserved.
func (s *IdentitySigner) signAt(sub string, iat, exp int64, traits map[string]any) (string, error) {
	header := map[string]any{"alg": "HS256", "kid": s.kid, "typ": "JWT"}
	payload := map[string]any{"sub": sub, "iat": iat, "exp": exp}
	if len(traits) > 0 {
		payload["traits"] = traits
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("kilden: traits are not JSON-serializable: %w", err)
	}

	b64 := base64.RawURLEncoding
	signingInput := b64.EncodeToString(headerJSON) + "." + b64.EncodeToString(payloadJSON)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(signingInput))
	return signingInput + "." + b64.EncodeToString(mac.Sum(nil)), nil
}
