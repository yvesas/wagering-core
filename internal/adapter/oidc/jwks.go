package oidc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// maxJWKSBytes bounds what the issuer can make us read. It is a remote document
// fetched on a schedule we do not control; without a ceiling, a misbehaving --
// or compromised -- endpoint decides how much memory this process uses.
const maxJWKSBytes = 1 << 20

// minRefreshInterval is how often an unknown key id may force a fetch.
//
// Key rotation has to be picked up without a restart, so an unknown kid
// refreshes the set. That is also a free denial of service if left unbounded: a
// token with a random kid would make us call the issuer, and a stream of them
// would make us call it continuously. The floor turns that into one request per
// interval.
//
// It is what a rotation costs, so it is short: five seconds of refusing tokens
// signed by a key that is minutes old, at most, against at most one extra
// request every five seconds when someone is making key ids up.
const minRefreshInterval = 5 * time.Second

// keySet is the issuer's public keys, cached.
type keySet struct {
	uri    string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	// One mutex, held across the fetch. It serialises everything, which is the
	// point: when a key expires under load, one request goes to the issuer and
	// the rest wait for it, rather than every in-flight verification opening
	// its own connection at the same moment.
	mu        sync.Mutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
}

func newKeySet(uri string, client *http.Client, ttl time.Duration, now func() time.Time) *keySet {
	return &keySet{uri: uri, client: client, ttl: ttl, now: now}
}

// key returns the public key with this id, fetching or refreshing as needed.
func (k *keySet) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.keys == nil || k.now().Sub(k.fetchedAt) >= k.ttl {
		if err := k.fetch(ctx); err != nil {
			// Nothing cached: there is no signature we can check, so the
			// failure is the answer.
			if k.keys == nil {
				return nil, err
			}
			// Otherwise the keys we already hold verify this signature
			// perfectly well, and refusing every request because the issuer is
			// unreachable would turn its outage into ours.
			//
			// The cost, stated plainly: a key the issuer retired stays usable
			// here for as long as it cannot be asked. Token expiry is what
			// bounds that, which is why an expiry is required.
			if key, ok := k.keys[kid]; ok {
				return key, nil
			}
			return nil, err
		}
	}

	if key, ok := k.keys[kid]; ok {
		return key, nil
	}

	// Unknown id on a fresh-enough set: the issuer may have rotated. Ask once
	// more, then give up -- see minRefreshInterval.
	if k.now().Sub(k.fetchedAt) >= minRefreshInterval {
		if err := k.fetch(ctx); err != nil {
			return nil, err
		}
		if key, ok := k.keys[kid]; ok {
			return key, nil
		}
	}
	return nil, fmt.Errorf("no signing key with id %q", kid)
}

// fetch reads the key set. The caller holds the mutex.
//
// A failed fetch leaves the previous keys in place rather than emptying the
// set. An issuer that is briefly unreachable should not invalidate every token
// this service can still verify perfectly well on its own.
func (k *keySet) fetch(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, k.uri, nil)
	if err != nil {
		return fmt.Errorf("building the jwks request: %w", err)
	}

	response, err := k.client.Do(request)
	if err != nil {
		return fmt.Errorf("fetching the jwks: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching the jwks: the issuer answered %s", response.Status)
	}

	var document struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxJWKSBytes)).Decode(&document); err != nil {
		return fmt.Errorf("decoding the jwks: %w", err)
	}

	keys := make(map[string]crypto.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		public, ok := key.public()
		if !ok {
			continue
		}
		keys[key.Kid] = public
	}
	if len(keys) == 0 {
		// Accepting an empty set would mean every token fails with "unknown
		// key", which reads as a client problem. It is not one.
		return fmt.Errorf("the jwks at %s has no usable signing key", k.uri)
	}

	k.keys = keys
	k.fetchedAt = k.now()
	return nil
}

// jsonWebKey is one entry of the key set, in the shape RFC 7517 defines.
type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`

	// RSA modulus and exponent, base64url without padding.
	N string `json:"n"`
	E string `json:"e"`
}

// public converts the entry into a key, reporting whether it is one we can use.
//
// Two whole categories are skipped rather than failed on. An encryption key --
// Keycloak publishes one next to the signing key -- is not a broken signing
// key, and a key type this build does not implement is the issuer offering more
// than we asked for. Failing the whole set over either would take the service
// down because of a key nobody was going to use.
func (k jsonWebKey) public() (crypto.PublicKey, bool) {
	if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
		return nil, false
	}
	if k.Kty != "RSA" {
		return nil, false
	}

	modulus, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil || len(modulus) == 0 {
		return nil, false
	}
	exponent, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(exponent) == 0 || len(exponent) > 8 {
		return nil, false
	}

	// The exponent is big-endian and short -- 65537 is three bytes. big.Int
	// reads it without caring how many, and Int64 is safe after the length
	// check above.
	e := new(big.Int).SetBytes(exponent).Int64()
	if e <= 0 || e > 1<<31 {
		return nil, false
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(e),
	}, true
}
