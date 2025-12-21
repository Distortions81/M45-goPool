package main

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultClerkIssuer            = "https://clerk.clerk.dev"
	defaultClerkJWKSURL           = "https://clerk.clerk.dev/.well-known/jwks"
	defaultClerkSessionCookieName = "__session"
	defaultClerkSignInURL         = "https://auth.clerk.dev/sign-in"
	defaultClerkCallbackPath      = "/clerk/callback"
)

type ClerkUser struct {
	UserID    string
	SessionID string
	Email     string
	FirstName string
	LastName  string
}

func (u *ClerkUser) DisplayName() string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		if u.Email != "" {
			return u.Email
		}
		return u.UserID
	}
	return name
}

type ClerkSessionClaims struct {
	jwt.RegisteredClaims
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type clerkJWKS struct {
	Keys []clerkJWK `json:"keys"`
}

type clerkJWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (j clerkJWK) rsaPublicKey() (*rsa.PublicKey, error) {
	if j.N == "" || j.E == "" {
		return nil, errors.New("missing rsa modulus or exponent")
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	eInt := 0
	for _, b := range eBytes {
		eInt = eInt<<8 + int(b)
	}
	if eInt == 0 {
		return nil, errors.New("invalid exponent")
	}
	pub := &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: eInt,
	}
	return pub, nil
}

type ClerkVerifier struct {
	client          *http.Client
	jwksURL         string
	issuer          string
	callbackPath    string
	sessionCookie   string
	signInURL       string
	keys            map[string]*rsa.PublicKey
	mu              sync.RWMutex
	lastKeyRefresh  time.Time
	keyRefreshLimit time.Duration
}

func NewClerkVerifier(cfg Config) (*ClerkVerifier, error) {
	jwksURL := strings.TrimSpace(cfg.ClerkJWKSURL)
	if jwksURL == "" {
		jwksURL = defaultClerkJWKSURL
	}
	issuer := strings.TrimSpace(cfg.ClerkIssuer)
	if issuer == "" {
		issuer = defaultClerkIssuer
	}
	sessionCookie := strings.TrimSpace(cfg.ClerkSessionCookieName)
	if sessionCookie == "" {
		sessionCookie = defaultClerkSessionCookieName
	}
	signInURL := strings.TrimSpace(cfg.ClerkSignInURL)
	if signInURL == "" {
		signInURL = defaultClerkSignInURL
	}
	callbackPath := strings.TrimSpace(cfg.ClerkCallbackPath)
	if callbackPath == "" {
		callbackPath = defaultClerkCallbackPath
	}

	v := &ClerkVerifier{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		jwksURL:         jwksURL,
		issuer:          issuer,
		callbackPath:    callbackPath,
		sessionCookie:   sessionCookie,
		signInURL:       signInURL,
		keyRefreshLimit: 5 * time.Minute,
	}
	if err := v.refreshKeys(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *ClerkVerifier) refreshKeys() error {
	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	var jwks clerkJWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return err
	}
	newKeys := make(map[string]*rsa.PublicKey)
	for _, key := range jwks.Keys {
		if key.Kty != "RSA" {
			continue
		}
		pub, err := key.rsaPublicKey()
		if err != nil {
			continue
		}
		if key.Kid != "" {
			newKeys[key.Kid] = pub
		}
	}
	if len(newKeys) == 0 {
		return errors.New("no rsa jwks found")
	}
	v.mu.Lock()
	v.keys = newKeys
	v.lastKeyRefresh = time.Now()
	v.mu.Unlock()
	return nil
}

func (v *ClerkVerifier) keyFor(kid string) *rsa.PublicKey {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.keys[kid]
}

func (v *ClerkVerifier) Verify(token string) (*ClerkSessionClaims, error) {
	if v == nil || token == "" {
		return nil, errors.New("missing session token")
	}
	claims := new(ClerkSessionClaims)
	keyFunc := func(t *jwt.Token) (interface{}, error) {
		kid, _ := t.Header["kid"].(string)
		pub := v.keyFor(kid)
		if pub == nil {
			if time.Since(v.lastKeyRefresh) > v.keyRefreshLimit {
				_ = v.refreshKeys()
				pub = v.keyFor(kid)
			}
		}
		if pub == nil {
			return nil, fmt.Errorf("unknown key %s", kid)
		}
		return pub, nil
	}
	tok, err := jwt.ParseWithClaims(token, claims, keyFunc, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(v.issuer))
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid session token")
	}
	return claims, nil
}

func (v *ClerkVerifier) LoginURL(r *http.Request, redirectPath string, frontendAPI string) string {
	if v == nil {
		return ""
	}
	var scheme string
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	} else {
		scheme = "http"
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	redirect := &url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   redirectPath,
	}
	values := url.Values{}
	values.Set("redirect_url", redirect.String())
	if frontendAPI != "" {
		values.Set("frontend_api", frontendAPI)
	}
	return v.signInURL + "?" + values.Encode()
}

func (v *ClerkVerifier) CallbackPath() string {
	if v == nil {
		return defaultClerkCallbackPath
	}
	return v.callbackPath
}

func (v *ClerkVerifier) SessionCookieName() string {
	if v == nil {
		return defaultClerkSessionCookieName
	}
	return v.sessionCookie
}

func ClerkUserFromContext(ctx context.Context) *ClerkUser {
	if ctx == nil {
		return nil
	}
	user, _ := ctx.Value(clerkContextKey{}).(*ClerkUser)
	return user
}

type clerkContextKey struct{}

func contextWithClerkUser(ctx context.Context, user *ClerkUser) context.Context {
	return context.WithValue(ctx, clerkContextKey{}, user)
}
