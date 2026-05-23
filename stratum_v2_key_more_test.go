package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/base58"
)

func TestLoadSV2KeyPairFromBase64RejectsEmptyAndInvalid(t *testing.T) {
	if _, err := loadSV2KeyPairFromBase64("   \n\t "); err == nil {
		t.Fatalf("expected empty input error")
	}
	if _, err := loadSV2KeyPairFromBase64("not_base64"); err == nil {
		t.Fatalf("expected decode error")
	}
}

func TestLoadSV2KeyWrappersFromBase64(t *testing.T) {
	raw := make([]byte, 32)
	raw[0] = 1
	raw[31] = 2
	enc := base64.StdEncoding.EncodeToString(raw)

	staticKey, err := loadSV2KeyFromBase64(enc)
	if err != nil {
		t.Fatalf("loadSV2KeyFromBase64: %v", err)
	}
	authKey, err := loadSV2AuthorityKeyFromBase64(enc)
	if err != nil {
		t.Fatalf("loadSV2AuthorityKeyFromBase64: %v", err)
	}
	if staticKey == nil || staticKey.privKey == nil || staticKey.pubKey == nil {
		t.Fatalf("static key is not populated")
	}
	if authKey == nil || authKey.privKey == nil || authKey.pubKey == nil {
		t.Fatalf("authority key is not populated")
	}
}

func TestLoadOrGenerateSV2KeyPairRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sv2", "key.bin")

	first, err := loadOrGenerateSV2KeyPair(path)
	if err != nil {
		t.Fatalf("first load/generate: %v", err)
	}
	if first == nil || first.privKey == nil || first.pubKey == nil {
		t.Fatalf("first key pair not populated")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("unexpected key file mode: got %o want 600", st.Mode().Perm())
	}

	second, err := loadOrGenerateSV2KeyPair(path)
	if err != nil {
		t.Fatalf("second load/generate: %v", err)
	}
	if second == nil || second.privKey == nil || second.pubKey == nil {
		t.Fatalf("second key pair not populated")
	}
	if !bytes.Equal(second.privKey.Serialize(), first.privKey.Serialize()) {
		t.Fatalf("expected persisted key to reload without regeneration")
	}
}

func TestLoadOrGenerateSV2KeyPairRegeneratesOnWrongLength(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "key.bin")
	if err := os.WriteFile(path, []byte{0x01, 0x02}, 0600); err != nil {
		t.Fatalf("write short key: %v", err)
	}
	pair, err := loadOrGenerateSV2KeyPair(path)
	if err != nil {
		t.Fatalf("load/generate: %v", err)
	}
	if pair == nil || pair.privKey == nil || pair.pubKey == nil {
		t.Fatalf("regenerated key pair not populated")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("expected regenerated key length 32, got %d", len(raw))
	}
}

func TestSV2KeyPublicHelpers(t *testing.T) {
	if (*sv2StaticKey)(nil).pubHex() != "" {
		t.Fatalf("nil static key pubHex must be empty")
	}
	if (*sv2AuthorityKey)(nil).pubHex() != "" {
		t.Fatalf("nil authority key pubHex must be empty")
	}
	if sv2AuthorityKeyFromStaticKey(nil) != nil {
		t.Fatalf("nil static key should produce nil authority key")
	}

	raw := make([]byte, 32)
	raw[31] = 3
	priv, pub := btcec.PrivKeyFromBytes(raw)
	staticKey := &sv2StaticKey{sv2KeyPair: sv2KeyPair{privKey: priv, pubKey: pub}}
	authority := sv2AuthorityKeyFromStaticKey(staticKey)
	if authority == nil || authority.pubKey == nil {
		t.Fatalf("expected authority key from static key")
	}
	if authority.pubHex() == "" {
		t.Fatalf("expected non-empty authority public hex")
	}
}

func TestSV2AuthorityKeyBase58CheckHelpers(t *testing.T) {
	if sv2AuthorityKeyBase58Check(nil) != "" {
		t.Fatalf("nil pubkey should encode as empty string")
	}
	if (&sv2AuthorityKey{}).authKeyBase58Check() != "" {
		t.Fatalf("nil authority pubkey should encode as empty string")
	}

	raw := make([]byte, 32)
	raw[30] = 0x11
	raw[31] = 0x22
	_, pub := btcec.PrivKeyFromBytes(raw)
	encoded := sv2AuthorityKeyBase58Check(pub)
	if strings.TrimSpace(encoded) == "" {
		t.Fatalf("expected non-empty base58check encoding")
	}
	decoded := base58.Decode(encoded)
	if len(decoded) != 38 {
		t.Fatalf("unexpected decoded length: got %d want 38", len(decoded))
	}
	if decoded[0] != 0x01 || decoded[1] != 0x00 {
		t.Fatalf("unexpected prefix bytes: %x %x", decoded[0], decoded[1])
	}
	payload := decoded[:34]
	checksum := decoded[34:]
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	if !strings.EqualFold(base64.StdEncoding.EncodeToString(checksum), base64.StdEncoding.EncodeToString(h2[:4])) {
		t.Fatalf("invalid checksum in base58check encoding")
	}
}
