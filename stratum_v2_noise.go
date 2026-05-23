package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ellswift"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const sv2NoiseProtocolNameEllSwift = "Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256"

type noiseState struct {
	ck     [32]byte
	h      [32]byte
	k      [32]byte
	n      uint64
	hasKey bool
}

func noiseInitializeSymmetric(protocolName string) noiseState {
	var s noiseState
	nameBytes := []byte(protocolName)
	if len(nameBytes) <= 32 {
		copy(s.ck[:], nameBytes)
	} else {
		s.ck = sha256.Sum256(nameBytes)
	}
	s.h = s.ck
	return s
}

func (s *noiseState) mixHash(data []byte) {
	h := sha256.New()
	h.Write(s.h[:])
	h.Write(data)
	copy(s.h[:], h.Sum(nil))
}

func noiseHKDF(ck [32]byte, ikm []byte) (ck2 [32]byte, k [32]byte) {
	r := hkdf.New(sha256.New, ikm, ck[:], nil)
	var out [64]byte
	if _, err := io.ReadFull(r, out[:]); err != nil {
		panic(fmt.Sprintf("sv2 hkdf: %v", err))
	}
	copy(ck2[:], out[:32])
	copy(k[:], out[32:])
	return
}

func (s *noiseState) mixKey(ikm []byte) {
	s.ck, s.k = noiseHKDF(s.ck, ikm)
	s.n = 0
	s.hasKey = true
}

func noiseNonce(n uint64) [12]byte {
	var nonce [12]byte
	binary.LittleEndian.PutUint64(nonce[4:], n)
	return nonce
}

func (s *noiseState) encryptAndHash(plaintext []byte) ([]byte, error) {
	if !s.hasKey {
		s.mixHash(plaintext)
		return plaintext, nil
	}
	aead, err := chacha20poly1305.New(s.k[:])
	if err != nil {
		return nil, err
	}
	nonce := noiseNonce(s.n)
	ciphertext := aead.Seal(nil, nonce[:], plaintext, s.h[:])
	s.mixHash(ciphertext)
	s.n++
	return ciphertext, nil
}

func (s *noiseState) decryptAndHash(ciphertext []byte) ([]byte, error) {
	if !s.hasKey {
		s.mixHash(ciphertext)
		return ciphertext, nil
	}
	aead, err := chacha20poly1305.New(s.k[:])
	if err != nil {
		return nil, err
	}
	nonce := noiseNonce(s.n)
	plaintext, err := aead.Open(nil, nonce[:], ciphertext, s.h[:])
	if err != nil {
		return nil, fmt.Errorf("noise decrypt: %w", err)
	}
	s.mixHash(ciphertext)
	s.n++
	return plaintext, nil
}

func (s *noiseState) split() (k1, k2 [32]byte) {
	k1, k2 = noiseHKDF(s.ck, nil)
	return
}

func noiseECDH(priv *btcec.PrivateKey, pub *btcec.PublicKey) []byte {
	return btcec.GenerateSharedSecret(priv, pub)
}

func sv2NoiseLogHexPrefix(step string, b []byte, n int) {
	if !logger.Enabled(logLevelDebug) {
		return
	}
	if n > len(b) {
		n = len(b)
	}
	logger.Debug("sv2 noise",
		"component", "stratum",
		"kind", "sv2_noise",
		"step", step,
		"hex", fmt.Sprintf("%x", b[:n]),
	)
}

// sv2NoiseHandshake performs the responder-side NX EllSwift handshake and returns an encrypted transport conn.
//
// TODO: this path is intentionally experimental until initiator-side authority
// certificate verification is implemented and exercised against official test
// vectors.
func sv2NoiseHandshake(conn net.Conn, staticKey *sv2StaticKey, authorityKey *sv2AuthorityKey) (net.Conn, error) {
	var eEll [64]byte
	if _, err := io.ReadFull(conn, eEll[:]); err != nil {
		return nil, fmt.Errorf("sv2 noise: read ellswift ephemeral pubkey: %w", err)
	}
	return sv2NoiseHandshakeEllSwift(conn, staticKey, authorityKey, eEll)
}

func sv2NoiseHandshakeEllSwift(conn net.Conn, staticKey *sv2StaticKey, authorityKey *sv2AuthorityKey, eEll [64]byte) (net.Conn, error) {
	s := noiseInitializeSymmetric(sv2NoiseProtocolNameEllSwift)
	// Empty prologue
	s.mixHash([]byte{})
	sv2NoiseLogHexPrefix("h_after_prologue", s.h[:], 32)
	sv2NoiseLogHexPrefix("e_pub", eEll[:], 16)
	s.mixHash(eEll[:])
	// Extra empty-payload mixHash after 'e' token, matching the SV2 initiator behavior.
	s.mixHash([]byte{})

	// Step 2: Generate responder ephemeral key as EllSwift and send it.
	re, reEll, err := ellswift.EllswiftCreate()
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: gen ephemeral key: %w", err)
	}
	if _, err := conn.Write(reEll[:]); err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: send ephemeral pubkey: %w", err)
	}
	sv2NoiseLogHexPrefix("re_pub", reEll[:], 16)
	s.mixHash(reEll[:])

	// Step 3: ee using BIP324 x-only hash over EllSwift keys.
	ee, err := ellswift.V2Ecdh(re, eEll, reEll, false)
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: ee ecdh: %w", err)
	}
	sv2NoiseLogHexPrefix("ecdh_shared", ee[:], 16)
	sv2NoiseLogHexPrefix("ck_before_hkdf", s.ck[:], 32)
	s.mixKey(ee[:])
	sv2NoiseLogHexPrefix("h_aad_for_encrypt_static", s.h[:], 32)
	sv2NoiseLogHexPrefix("ck_after_hkdf", s.ck[:], 32)
	sv2NoiseLogHexPrefix("temp_k_encrypt_static", s.k[:], 32)

	// Step 4: Encrypt and send responder static pubkey encoded as EllSwift (64 bytes).
	rsStaticEll, xOnly, err := sv2EncodeStaticEllSwift(staticKey)
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: encode static pubkey: %w", err)
	}
	encStatic, err := s.encryptAndHash(rsStaticEll[:])
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: encrypt static: %w", err)
	}
	sv2NoiseLogHexPrefix("enc_static_first32", encStatic, 32)
	if len(encStatic) >= chacha20poly1305.Overhead {
		sv2NoiseLogHexPrefix("enc_static_mac", encStatic[len(encStatic)-chacha20poly1305.Overhead:], chacha20poly1305.Overhead)
	}
	if _, err := conn.Write(encStatic); err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: send static: %w", err)
	}

	// Step 5: es using BIP324 x-only hash over initiator ephemeral and responder static EllSwift.
	es, err := ellswift.V2Ecdh(staticKey.privKey, eEll, rsStaticEll, false)
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: es ecdh: %w", err)
	}
	s.mixKey(es[:])

	// Step 6: Encrypt and send server certificate payload (74 bytes plaintext, 90 bytes ciphertext).
	if authorityKey == nil {
		authorityKey = sv2AuthorityKeyFromStaticKey(staticKey)
	}
	certPayload, err := sv2BuildServerCertPayload(authorityKey, xOnly)
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: build cert payload: %w", err)
	}
	encCert, err := s.encryptAndHash(certPayload[:])
	if err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: encrypt cert payload: %w", err)
	}
	if _, err := conn.Write(encCert); err != nil {
		return nil, fmt.Errorf("sv2 noise ellswift: send cert payload: %w", err)
	}

	// Step 7: Split into transport keys.
	k1, k2 := s.split()

	return &sv2EncryptedFrameConn{
		Conn:    conn,
		sendKey: k2,
		recvKey: k1,
	}, nil
}

// sv2EncryptedFrameConn implements SV2 frame-oriented encrypted transport used
// by the EllSwift client path (encrypted header then encrypted payload).
type sv2EncryptedFrameConn struct {
	net.Conn
	sendKey [32]byte
	recvKey [32]byte
	sendN   uint64
	recvN   uint64
	sendMu  sync.Mutex
	recvMu  sync.Mutex
}

func sv2EncodeStaticEllSwift(staticKey *sv2StaticKey) ([64]byte, [32]byte, error) {
	var out [64]byte
	var xOnly [32]byte

	comp := staticKey.pubKey.SerializeCompressed()
	copy(xOnly[:], comp[1:33])

	var x btcec.FieldVal
	overflow := x.SetByteSlice(xOnly[:])
	if overflow {
		x.Normalize()
	}
	u, t, err := ellswift.XElligatorSwift(&x)
	if err != nil {
		return out, xOnly, err
	}
	uBytes := u.Bytes()
	tBytes := t.Bytes()
	copy(out[0:32], (*uBytes)[:])
	copy(out[32:64], (*tBytes)[:])
	return out, xOnly, nil
}

func sv2BuildServerCertPayload(authorityKey *sv2AuthorityKey, staticXOnly [32]byte) ([74]byte, error) {
	var payload [74]byte
	binary.LittleEndian.PutUint16(payload[0:2], 0x0001)
	now := time.Now().UTC()
	validFrom := uint32(now.Unix())
	validTo := uint32(now.Add(365 * 24 * time.Hour).Unix())
	binary.LittleEndian.PutUint32(payload[2:6], validFrom)
	binary.LittleEndian.PutUint32(payload[6:10], validTo)

	h := sha256.New()
	h.Write(payload[:10])
	h.Write(staticXOnly[:])
	sigHash := h.Sum(nil)
	if authorityKey == nil || authorityKey.privKey == nil {
		return payload, fmt.Errorf("sv2 certificate authority key is not configured")
	}
	sig, err := schnorr.Sign(authorityKey.privKey, sigHash)
	if err != nil {
		return payload, err
	}
	copy(payload[10:], sig.Serialize())
	return payload, nil
}

func (c *sv2EncryptedFrameConn) Write(p []byte) (int, error) {
	return 0, fmt.Errorf("sv2 frame conn: raw Write unsupported")
}

func (c *sv2EncryptedFrameConn) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("sv2 frame conn: raw Read unsupported")
}

func (c *sv2EncryptedFrameConn) WriteSV2Frame(msgType byte, payload []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if len(payload) > sv2PlaintextFramePayloadMax-16 {
		return fmt.Errorf("sv2 frame payload too large: %d", len(payload))
	}

	header := [6]byte{0, 0, msgType, byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16)}
	encHeader, err := sv2EncryptWithKey(c.sendKey, c.sendN, header[:])
	if err != nil {
		return err
	}
	c.sendN++
	if _, err := c.Conn.Write(encHeader); err != nil {
		return err
	}

	if len(payload) == 0 {
		return nil
	}
	encPayload, err := sv2EncryptWithKey(c.sendKey, c.sendN, payload)
	if err != nil {
		return err
	}
	c.sendN++
	_, err = c.Conn.Write(encPayload)
	return err
}

func (c *sv2EncryptedFrameConn) ReadSV2Frame() (byte, []byte, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	encHeader := make([]byte, 22)
	if _, err := io.ReadFull(c.Conn, encHeader); err != nil {
		return 0, nil, err
	}
	header, err := sv2DecryptWithKey(c.recvKey, c.recvN, encHeader)
	if err != nil {
		return 0, nil, fmt.Errorf("sv2 frame decrypt header: %w", err)
	}
	c.recvN++
	if len(header) != 6 {
		return 0, nil, fmt.Errorf("sv2 frame header len %d", len(header))
	}
	ext := binary.LittleEndian.Uint16(header[0:2])
	sv2ObserveExtensionType(ext, "encrypted")

	msgType := header[2]
	payLen := uint32(header[3]) | uint32(header[4])<<8 | uint32(header[5])<<16
	if payLen == 0 {
		return msgType, nil, nil
	}
	if payLen > sv2PlaintextFramePayloadMax-16 {
		return 0, nil, fmt.Errorf("sv2 frame payload too large: %d", payLen)
	}

	encPayload := make([]byte, int(payLen)+chacha20poly1305.Overhead)
	if _, err := io.ReadFull(c.Conn, encPayload); err != nil {
		return 0, nil, err
	}
	payload, err := sv2DecryptWithKey(c.recvKey, c.recvN, encPayload)
	if err != nil {
		return 0, nil, fmt.Errorf("sv2 frame decrypt payload: %w", err)
	}
	c.recvN++
	return msgType, payload, nil
}

func sv2EncryptWithKey(key [32]byte, n uint64, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	nonce := noiseNonce(n)
	return aead.Seal(nil, nonce[:], plaintext, nil), nil
}

func sv2DecryptWithKey(key [32]byte, n uint64, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	nonce := noiseNonce(n)
	return aead.Open(nil, nonce[:], ciphertext, nil)
}
