package main

import (
	"encoding/base64"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/base58"
)

type sv2KeyPair struct {
	privKey *btcec.PrivateKey
	pubKey  *btcec.PublicKey
}

type sv2StaticKey struct {
	sv2KeyPair
}

type sv2AuthorityKey struct {
	sv2KeyPair
}

func loadOrGenerateSV2Key(path string) (*sv2StaticKey, error) {
	pair, err := loadOrGenerateSV2KeyPair(path)
	if err != nil {
		return nil, err
	}
	return &sv2StaticKey{sv2KeyPair: *pair}, nil
}

func loadOrGenerateSV2AuthorityKey(path string) (*sv2AuthorityKey, error) {
	pair, err := loadOrGenerateSV2KeyPair(path)
	if err != nil {
		return nil, err
	}
	return &sv2AuthorityKey{sv2KeyPair: *pair}, nil
}

func loadSV2KeyFromBase64(encoded string) (*sv2StaticKey, error) {
	pair, err := loadSV2KeyPairFromBase64(encoded)
	if err != nil {
		return nil, err
	}
	return &sv2StaticKey{sv2KeyPair: *pair}, nil
}

func loadSV2AuthorityKeyFromBase64(encoded string) (*sv2AuthorityKey, error) {
	pair, err := loadSV2KeyPairFromBase64(encoded)
	if err != nil {
		return nil, err
	}
	return &sv2AuthorityKey{sv2KeyPair: *pair}, nil
}

func loadSV2KeyPairFromBase64(encoded string) (*sv2KeyPair, error) {
	raw := strings.TrimSpace(encoded)
	if raw == "" {
		return nil, fmt.Errorf("sv2 key base64 is empty")
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("sv2 key base64 decode: %w", err)
	}
	if len(data) != 32 {
		return nil, fmt.Errorf("sv2 key base64 decoded length %d, want 32", len(data))
	}
	privKey, pubKey := btcec.PrivKeyFromBytes(data)
	return &sv2KeyPair{privKey: privKey, pubKey: pubKey}, nil
}

func loadOrGenerateSV2KeyPair(path string) (*sv2KeyPair, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("sv2 key dir: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil && len(data) == 32 {
		privKey, pubKey := btcec.PrivKeyFromBytes(data)
		return &sv2KeyPair{privKey: privKey, pubKey: pubKey}, nil
	}
	// Generate new key
	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("sv2 key gen: %w", err)
	}
	keyBytes := privKey.Serialize() // 32-byte big-endian scalar
	if err := os.WriteFile(path, keyBytes, 0600); err != nil {
		return nil, fmt.Errorf("sv2 key save: %w", err)
	}
	return &sv2KeyPair{privKey: privKey, pubKey: privKey.PubKey()}, nil
}

func (k *sv2StaticKey) pubHex() string {
	if k == nil || k.pubKey == nil {
		return ""
	}
	return hex.EncodeToString(k.pubKey.SerializeCompressed())
}

func (k *sv2AuthorityKey) pubHex() string {
	if k == nil || k.pubKey == nil {
		return ""
	}
	return hex.EncodeToString(k.pubKey.SerializeCompressed())
}

func sv2AuthorityKeyFromStaticKey(staticKey *sv2StaticKey) *sv2AuthorityKey {
	if staticKey == nil {
		return nil
	}
	return &sv2AuthorityKey{sv2KeyPair: staticKey.sv2KeyPair}
}

// sv2AuthorityKeyBase58Check encodes a 32-byte x-only secp256k1 public key in
// the SV2 URL format used for the Pool Authority Key.
func sv2AuthorityKeyBase58Check(pubKey *btcec.PublicKey) string {
	if pubKey == nil {
		return ""
	}
	xBytes := pubKey.X().Bytes()
	xOnly := make([]byte, 32)
	copy(xOnly[32-len(xBytes):], xBytes)
	payload := make([]byte, 34)
	payload[0] = 0x01
	payload[1] = 0x00
	copy(payload[2:], xOnly)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	return base58.Encode(append(payload, h2[:4]...))
}

func (k *sv2AuthorityKey) authKeyBase58Check() string {
	return sv2AuthorityKeyBase58Check(k.pubKey)
}
