package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ellswift"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	sv2NoiseAct1Len = 64
	sv2NoiseAct2Len = 234

	sv2NoiseEncryptedHeaderLen = stratumV2FrameHeaderLen + 16 // 6 + Poly1305 tag
	sv2NoiseCertPayloadLen     = 74
)

const sv2NoiseProtocolName = "Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256"

// SHA-256("Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256"), captured from the
// ESP-Miner reference implementation for cross-checking during future work.
var sv2NoiseNXProtocolHashSHA256 = [32]byte{
	46, 180, 120, 129, 32, 142, 158, 238, 31, 102, 159, 103, 198, 110, 231, 14,
	169, 234, 136, 9, 13, 80, 63, 232, 48, 220, 75, 200, 62, 41, 191, 16,
}

type sv2NoiseHandshakeState string

const (
	sv2NoiseHandshakeInit        sv2NoiseHandshakeState = "init"
	sv2NoiseHandshakeAct1Read    sv2NoiseHandshakeState = "act1-read"
	sv2NoiseHandshakeAct2Sent    sv2NoiseHandshakeState = "act2-sent"
	sv2NoiseHandshakeComplete    sv2NoiseHandshakeState = "complete"
	sv2NoiseHandshakeUnsupported sv2NoiseHandshakeState = "unsupported"
)

type sv2NoiseResponderHandshake struct {
	r     io.Reader
	w     io.Writer
	state sv2NoiseHandshakeState

	recvKey [32]byte // initiator -> responder
	sendKey [32]byte // responder -> initiator
}

func newSV2NoiseResponderHandshake(r io.Reader, w io.Writer) *sv2NoiseResponderHandshake {
	return &sv2NoiseResponderHandshake{r: r, w: w, state: sv2NoiseHandshakeInit}
}

func (h *sv2NoiseResponderHandshake) State() sv2NoiseHandshakeState {
	if h == nil {
		return sv2NoiseHandshakeUnsupported
	}
	return h.state
}

func (h *sv2NoiseResponderHandshake) RecvKey() [32]byte { return h.recvKey }
func (h *sv2NoiseResponderHandshake) SendKey() [32]byte { return h.sendKey }

// Perform executes a responder-side Noise_NX handshake compatible with the
// ESP-Miner reference client. Certificate verification/signing authority is not
// yet wired; the encrypted certificate payload is still emitted in TOFU style.
func (h *sv2NoiseResponderHandshake) Perform() error {
	if h == nil {
		return errSV2NoiseHandshakeNotImplemented
	}
	if h.state == sv2NoiseHandshakeComplete {
		return nil
	}
	if h.r == nil || h.w == nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise handshake missing transport")
	}

	var initiatorE [sv2NoiseAct1Len]byte
	if _, err := io.ReadFull(h.r, initiatorE[:]); err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise read act1: %w", err)
	}
	h.state = sv2NoiseHandshakeAct1Read

	hs := sv2NoiseNewHandshakeHash()
	// Process initiator act1: token e + EncryptAndHash(empty)
	sv2NoiseMixHash(&hs.h, initiatorE[:])
	sv2NoiseMixHash(&hs.h, nil)

	// Responder ephemeral keypair (re)
	rePriv, reEnc, err := ellswift.EllswiftCreate()
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise responder ephemeral: %w", err)
	}
	sv2NoiseMixHash(&hs.h, reEnc[:])

	// ee
	ee, err := ellswift.V2Ecdh(rePriv, initiatorE, reEnc, false)
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise ee v2ecdh: %w", err)
	}
	var tempK1 [32]byte
	sv2NoiseHKDF2(&hs.ck, (*ee)[:], &hs.ck, &tempK1)

	// Responder static key (rs) - currently per-connection TOFU
	rsPriv, rsEnc, err := ellswift.EllswiftCreate()
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise responder static: %w", err)
	}
	encStatic, err := sv2NoiseEncrypt(tempK1, 0, hs.h[:], rsEnc[:])
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise encrypt static: %w", err)
	}
	sv2NoiseMixHash(&hs.h, encStatic)

	// es
	es, err := ellswift.V2Ecdh(rsPriv, initiatorE, rsEnc, false)
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise es v2ecdh: %w", err)
	}
	var tempK2 [32]byte
	sv2NoiseHKDF2(&hs.ck, (*es)[:], &hs.ck, &tempK2)

	certPayload, err := sv2NoiseBuildTOFUCertPayload(rsPriv)
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise cert payload: %w", err)
	}
	encCert, err := sv2NoiseEncrypt(tempK2, 0, hs.h[:], certPayload)
	if err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise encrypt cert: %w", err)
	}
	sv2NoiseMixHash(&hs.h, encCert)

	var act2 [sv2NoiseAct2Len]byte
	copy(act2[0:64], reEnc[:])
	copy(act2[64:144], encStatic)
	copy(act2[144:234], encCert)
	if err := sv2NoiseWriteAll(h.w, act2[:]); err != nil {
		h.state = sv2NoiseHandshakeUnsupported
		return fmt.Errorf("sv2 noise write act2: %w", err)
	}
	h.state = sv2NoiseHandshakeAct2Sent

	// Key split: c1 = initiator->responder, c2 = responder->initiator
	var c1, c2 [32]byte
	sv2NoiseHKDF2(&hs.ck, nil, &c1, &c2)
	h.recvKey = c1
	h.sendKey = c2
	h.state = sv2NoiseHandshakeComplete
	return nil
}

type sv2NoiseFrameTransport struct {
	r         io.Reader
	w         io.Writer
	handshake *sv2NoiseResponderHandshake

	recvKey [32]byte
	sendKey [32]byte

	recvNonce uint64
	sendNonce uint64
}

func newSV2NoiseFrameTransport(r io.Reader, w io.Writer) *sv2NoiseFrameTransport {
	return &sv2NoiseFrameTransport{
		r:         r,
		w:         w,
		handshake: newSV2NoiseResponderHandshake(r, w),
	}
}

func (t *sv2NoiseFrameTransport) Mode() string { return "noise" }

func (t *sv2NoiseFrameTransport) ensureHandshake() error {
	if t == nil || t.handshake == nil {
		return errSV2NoiseHandshakeNotImplemented
	}
	if t.handshake.State() != sv2NoiseHandshakeComplete {
		if err := t.handshake.Perform(); err != nil {
			return err
		}
		t.recvKey = t.handshake.RecvKey()
		t.sendKey = t.handshake.SendKey()
		t.recvNonce = 0
		t.sendNonce = 0
	}
	return nil
}

func (t *sv2NoiseFrameTransport) ReadFrame() ([]byte, error) {
	if err := t.ensureHandshake(); err != nil {
		return nil, err
	}
	var encHdr [sv2NoiseEncryptedHeaderLen]byte
	if _, err := io.ReadFull(t.r, encHdr[:]); err != nil {
		return nil, err
	}
	hdr, err := sv2NoiseDecrypt(t.recvKey, t.recvNonce, nil, encHdr[:])
	if err != nil {
		return nil, fmt.Errorf("sv2 noise decrypt header: %w", err)
	}
	t.recvNonce++
	if len(hdr) != stratumV2FrameHeaderLen {
		return nil, fmt.Errorf("sv2 noise decrypted header len=%d want %d", len(hdr), stratumV2FrameHeaderLen)
	}
	payloadLen := int(readUint24LE(hdr[3:6]))
	frame := make([]byte, stratumV2FrameHeaderLen+payloadLen)
	copy(frame[:stratumV2FrameHeaderLen], hdr)
	if payloadLen == 0 {
		return frame, nil
	}
	encPayload := make([]byte, payloadLen+16)
	if _, err := io.ReadFull(t.r, encPayload); err != nil {
		return nil, err
	}
	payload, err := sv2NoiseDecrypt(t.recvKey, t.recvNonce, nil, encPayload)
	if err != nil {
		return nil, fmt.Errorf("sv2 noise decrypt payload: %w", err)
	}
	t.recvNonce++
	if len(payload) != payloadLen {
		return nil, fmt.Errorf("sv2 noise decrypted payload len=%d want %d", len(payload), payloadLen)
	}
	copy(frame[stratumV2FrameHeaderLen:], payload)
	return frame, nil
}

func (t *sv2NoiseFrameTransport) WriteFrame(frame []byte) error {
	if err := t.ensureHandshake(); err != nil {
		return err
	}
	if len(frame) < stratumV2FrameHeaderLen {
		return fmt.Errorf("sv2 noise frame too short: %d", len(frame))
	}
	encHdr, err := sv2NoiseEncrypt(t.sendKey, t.sendNonce, nil, frame[:stratumV2FrameHeaderLen])
	if err != nil {
		return fmt.Errorf("sv2 noise encrypt header: %w", err)
	}
	t.sendNonce++
	if err := sv2NoiseWriteAll(t.w, encHdr); err != nil {
		return err
	}
	if len(frame) == stratumV2FrameHeaderLen {
		return nil
	}
	encPayload, err := sv2NoiseEncrypt(t.sendKey, t.sendNonce, nil, frame[stratumV2FrameHeaderLen:])
	if err != nil {
		return fmt.Errorf("sv2 noise encrypt payload: %w", err)
	}
	t.sendNonce++
	return sv2NoiseWriteAll(t.w, encPayload)
}

type sv2NoiseHandshakeHashState struct {
	h  [32]byte
	ck [32]byte
}

func sv2NoiseNewHandshakeHash() sv2NoiseHandshakeHashState {
	sum := sha256.Sum256([]byte(sv2NoiseProtocolName))
	hs := sv2NoiseHandshakeHashState{h: sum, ck: sum}
	// Empty prologue required by Noise before processing tokens.
	sv2NoiseMixHash(&hs.h, nil)
	return hs
}

func sv2NoiseMixHash(h *[32]byte, data []byte) {
	sum := sha256.New()
	_, _ = sum.Write(h[:])
	if len(data) > 0 {
		_, _ = sum.Write(data)
	}
	out := sum.Sum(nil)
	copy(h[:], out[:32])
}

func sv2NoiseHKDF2(ck *[32]byte, ikm []byte, out1 *[32]byte, out2 *[32]byte) {
	prk := sv2NoiseHMACSHA256(ck[:], ikm)
	t1 := sv2NoiseHMACSHA256(prk[:], []byte{0x01})
	var t2Input [33]byte
	copy(t2Input[:32], t1[:])
	t2Input[32] = 0x02
	t2 := sv2NoiseHMACSHA256(prk[:], t2Input[:])
	if out1 != nil {
		copy(out1[:], t1[:])
	}
	if out2 != nil {
		copy(out2[:], t2[:])
	}
}

func sv2NoiseHMACSHA256(key []byte, msg []byte) [32]byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(msg)
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out
}

func sv2NoiseEncrypt(key [32]byte, nonce uint64, aad []byte, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	nonceBytes := sv2NoiseNonce(nonce)
	out := aead.Seal(nil, nonceBytes[:], plaintext, aad)
	return out, nil
}

func sv2NoiseDecrypt(key [32]byte, nonce uint64, aad []byte, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	nonceBytes := sv2NoiseNonce(nonce)
	return aead.Open(nil, nonceBytes[:], ciphertext, aad)
}

func sv2NoiseNonce(counter uint64) [12]byte {
	var nonce [12]byte
	binary.LittleEndian.PutUint64(nonce[4:], counter)
	return nonce
}

func sv2NoiseWriteAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func sv2NoiseBuildTOFUCertPayload(rsPriv *btcec.PrivateKey) ([]byte, error) {
	if rsPriv == nil {
		return nil, fmt.Errorf("nil responder static key")
	}
	payload := make([]byte, sv2NoiseCertPayloadLen)
	// version(u16 LE), valid_from(u32 LE), not_valid_after(u32 LE)
	binary.LittleEndian.PutUint16(payload[0:2], 0)
	now := time.Now().UTC()
	validFrom := uint32(now.Add(-1 * time.Hour).Unix())
	notAfter := uint32(now.Add(365 * 24 * time.Hour).Unix())
	binary.LittleEndian.PutUint32(payload[2:6], validFrom)
	binary.LittleEndian.PutUint32(payload[6:10], notAfter)

	// Build a TOFU/self-signed certificate payload. ESP-Miner skips verification
	// when no authority pubkey is configured, but emitting a structurally valid
	// signature keeps the payload well-formed and easier to debug.
	msgHash := sha256.Sum256(append(append(append(
		append([]byte{}, payload[0:2]...),
		payload[2:6]...),
		payload[6:10]...),
		schnorr.SerializePubKey(rsPriv.PubKey())...))
	sig, err := schnorr.Sign(rsPriv, msgHash[:])
	if err != nil {
		// Fallback to zero signature; TOFU clients without authority pinning
		// should still accept the handshake.
		return payload, nil
	}
	copy(payload[10:74], sig.Serialize())
	return payload, nil
}
