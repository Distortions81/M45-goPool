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

	clerk "github.com/clerk/clerk-sdk-go/v2"
	clerkclient "github.com/clerk/clerk-sdk-go/v2/client"
	clerksession "github.com/clerk/clerk-sdk-go/v2/session"
	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultClerkIssuerURL         = "https://clerk.clerk.dev"
	defaultClerkJWKSURL           = "https://clerk.clerk.dev/.well-known/jwks"
	defaultClerkSessionCookieName = "__session"
	defaultClerkSignInURL         = "https://auth.clerk.dev/sign-in"
	defaultClerkCallbackPath      = "/clerk/callback"
	clerkDevBrowserJWTQueryParam  = "__clerk_db_jwt"
)

type ClerkUser struct {
	UserID    string
	SessionID string
}

type ClerkSessionClaims struct {
	jwt.RegisteredClaims
	Version   int    `json:"v,omitempty"`
	SessionID string `json:"sid"`
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
	secretKey       string
	clerkClients    *clerkclient.Client
	clerkSessions   *clerksession.Client
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
	issuer := strings.TrimSpace(cfg.ClerkIssuerURL)
	if issuer == "" {
		issuer = defaultClerkIssuerURL
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
	secretKey := strings.TrimSpace(cfg.ClerkSecretKey)

	v := &ClerkVerifier{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		jwksURL:         jwksURL,
		issuer:          issuer,
		callbackPath:    callbackPath,
		sessionCookie:   sessionCookie,
		signInURL:       signInURL,
		secretKey:       secretKey,
		keyRefreshLimit: 5 * time.Minute,
	}
	if secretKey != "" {
		cc := &clerk.ClientConfig{}
		cc.Key = clerk.String(secretKey)
		v.clerkClients = clerkclient.NewClient(cc)
		v.clerkSessions = clerksession.NewClient(cc)
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

// ExchangeDevBrowserJWT exchanges Clerk's development-only __clerk_db_jwt query
// parameter for a short-lived session JWT (which can then be stored in the
// normal __session cookie and verified networklessly via JWKS).
func (v *ClerkVerifier) ExchangeDevBrowserJWT(ctx context.Context, devBrowserJWT string) (string, *ClerkSessionClaims, error) {
	if v == nil {
		return "", nil, errors.New("clerk verifier not configured")
	}
	devBrowserJWT = strings.TrimSpace(devBrowserJWT)
	if devBrowserJWT == "" {
		return "", nil, errors.New("missing dev browser jwt")
	}
	if v.secretKey == "" || v.clerkClients == nil || v.clerkSessions == nil {
		return "", nil, errors.New("clerk_secret_key not configured")
	}
	cl, err := v.clerkClients.Verify(ctx, &clerkclient.VerifyParams{Token: clerk.String(devBrowserJWT)})
	if err != nil {
		return "", nil, fmt.Errorf("verify dev browser jwt: %w", err)
	}
	var sessionID string
	if cl != nil && cl.LastActiveSessionID != nil {
		sessionID = strings.TrimSpace(*cl.LastActiveSessionID)
	}
	if sessionID == "" && cl != nil && len(cl.Sessions) > 0 {
		sessionID = strings.TrimSpace(cl.Sessions[0].ID)
	}
	if sessionID == "" && cl != nil && len(cl.SessionIDs) > 0 {
		sessionID = strings.TrimSpace(cl.SessionIDs[0])
	}
	if sessionID == "" {
		return "", nil, errors.New("no active session in dev browser token")
	}
	expiresIn := int64(clerkDevSessionTokenTTL / time.Second)
	if expiresIn <= 0 {
		expiresIn = 60 * 60
	}
	tok, err := v.clerkSessions.CreateToken(ctx, &clerksession.CreateTokenParams{
		ID:               sessionID,
		ExpiresInSeconds: clerk.Int64(expiresIn),
	})
	if err != nil {
		return "", nil, fmt.Errorf("create session token: %w", err)
	}
	jwtToken := strings.TrimSpace(tok.JWT)
	if jwtToken == "" {
		return "", nil, errors.New("missing session jwt")
	}
	claims, err := v.Verify(jwtToken)
	if err != nil {
		return "", nil, fmt.Errorf("verify exchanged session token: %w", err)
	}
	return jwtToken, claims, nil
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
