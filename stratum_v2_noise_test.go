package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ellswift"
	"golang.org/x/crypto/chacha20poly1305"
)
// testNoiseInitiator implements the NX initiator side for testing.
func testNoiseInitiator(conn net.Conn) (net.Conn, error) {
	s := noiseInitializeSymmetric(sv2NoiseProtocolNameEllSwift)
	s.mixHash([]byte{})

	// Generate EllSwift initiator ephemeral key.
	ePriv, ePubEll, err := ellswift.EllswiftCreate()
	if err != nil {
		return nil, err
	}
	s.mixHash(ePubEll[:])
	// Extra empty-payload mixHash after 'e' token (matches server/SV2 spec).
	s.mixHash([]byte{})

	if _, err := conn.Write(ePubEll[:]); err != nil {
		return nil, err
	}

	var rePubEll [64]byte
	if _, err := io.ReadFull(conn, rePubEll[:]); err != nil {
		return nil, err
	}
	s.mixHash(rePubEll[:])

	ee, err := ellswift.V2Ecdh(ePriv, rePubEll, ePubEll, true)
	if err != nil {
		return nil, err
	}
	s.mixKey(ee[:])

	// Read encrypted static (80 bytes = 64 + tag).
	encStatic := make([]byte, 64+chacha20poly1305.Overhead)
	if _, err := io.ReadFull(conn, encStatic); err != nil {
		return nil, err
	}
	rsStaticEll, err := s.decryptAndHash(encStatic)
	if err != nil {
		return nil, err
	}
	if len(rsStaticEll) != 64 {
		return nil, io.ErrUnexpectedEOF
	}
	var rsStatic [64]byte
	copy(rsStatic[:], rsStaticEll)

	es, err := ellswift.V2Ecdh(ePriv, rsStatic, ePubEll, true)
	if err != nil {
		return nil, err
	}
	s.mixKey(es[:])

	// Read encrypted certificate payload (90 bytes = 74 + tag).
	encCert := make([]byte, 74+chacha20poly1305.Overhead)
	if _, err := io.ReadFull(conn, encCert); err != nil {
		return nil, err
	}
	certPayload, err := s.decryptAndHash(encCert)
	if err != nil {
		return nil, err
	}
	if len(certPayload) != 74 {
		return nil, io.ErrUnexpectedEOF
	}
	_ = binary.LittleEndian.Uint16(certPayload[0:2])

	k1, k2 := s.split()
	return &sv2EncryptedFrameConn{
		Conn:    conn,
		sendKey: k1,
		recvKey: k2,
	}, nil
}

func TestSV2NoiseHandshakeEllSwift(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	staticKey := &sv2StaticKey{sv2KeyPair: sv2KeyPair{privKey: privKey, pubKey: privKey.PubKey()}}

	errCh := make(chan error, 2)
	type result struct {
		conn net.Conn
		err  error
	}
	serverCh := make(chan result, 1)
	clientCh := make(chan result, 1)

	go func() {
		authorityKey := sv2AuthorityKeyFromStaticKey(staticKey)
		conn, err := sv2NoiseHandshake(serverConn, staticKey, authorityKey)
		serverCh <- result{conn, err}
	}()
	go func() {
		conn, err := testNoiseInitiator(clientConn)
		clientCh <- result{conn, err}
	}()

	sr := <-serverCh
	cr := <-clientCh
	if sr.err != nil {
		t.Fatal("server handshake error:", sr.err)
	}
	if cr.err != nil {
		t.Fatal("client handshake error:", cr.err)
	}

	serverEnc, ok := sr.conn.(interface{ WriteSV2Frame(msgType byte, payload []byte) error })
	if !ok {
		t.Fatal("server transport does not implement frame writer")
	}
	clientEnc, ok := cr.conn.(interface{ ReadSV2Frame() (byte, []byte, error) })
	if !ok {
		t.Fatal("client transport does not implement frame reader")
	}

	// Test encrypted communication: server frame -> client frame.
	msgType := byte(0x42)
	msgPayload := []byte("hello sv2 ellswift")
	go func() {
		errCh <- serverEnc.WriteSV2Frame(msgType, msgPayload)
	}()
	go func() {
		mt, payload, err := clientEnc.ReadSV2Frame()
		if err == nil && (mt != msgType || string(payload) != string(msgPayload)) {
			err = io.ErrUnexpectedEOF
		}
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatal("transport error:", err)
		}
	}
}
