package main

import (
	"errors"
	"fmt"
	"io"
)

var errSV2NoiseHandshakeNotImplemented = errors.New("sv2 noise handshake not implemented")

type sv2FrameTransport interface {
	ReadFrame() ([]byte, error)
	WriteFrame([]byte) error
	Mode() string
}

type sv2PlainFrameTransport struct {
	r io.Reader
	w io.Writer
}

func (t *sv2PlainFrameTransport) ReadFrame() ([]byte, error) {
	if t == nil {
		return nil, io.EOF
	}
	return readOneStratumV2FrameFromReader(t.r)
}

func (t *sv2PlainFrameTransport) WriteFrame(frame []byte) error {
	if t == nil {
		return io.ErrClosedPipe
	}
	_, err := t.w.Write(frame)
	return err
}

func (t *sv2PlainFrameTransport) Mode() string { return "plaintext" }

type sv2NoiseFrameTransportStub struct {
	cause string
}

func (t *sv2NoiseFrameTransportStub) ReadFrame() ([]byte, error) {
	if t == nil || t.cause == "" {
		return nil, errSV2NoiseHandshakeNotImplemented
	}
	return nil, fmt.Errorf("%w: %s", errSV2NoiseHandshakeNotImplemented, t.cause)
}

func (t *sv2NoiseFrameTransportStub) WriteFrame([]byte) error {
	if t == nil || t.cause == "" {
		return errSV2NoiseHandshakeNotImplemented
	}
	return fmt.Errorf("%w: %s", errSV2NoiseHandshakeNotImplemented, t.cause)
}

func (t *sv2NoiseFrameTransportStub) Mode() string { return "noise" }

type sv2TransportDetection struct {
	mode  string
	cause string
}

// detectSV2TransportMode heuristically distinguishes raw plaintext SV2 frame
// traffic from SV2+Noise peers before the first frame is consumed.
//
// Today goPool only implements plaintext SV2 framing. ESP-Miner's SV2 branch
// starts with a Noise handshake, so surfacing this clearly helps diagnose why a
// miner connects and then disconnects without receiving jobs.
func detectSV2TransportMode(r io.Reader) sv2TransportDetection {
	peeker, ok := r.(interface{ Peek(int) ([]byte, error) })
	if !ok {
		return sv2TransportDetection{mode: "plaintext", cause: "reader-not-peekable"}
	}
	hdr, err := peeker.Peek(stratumV2FrameHeaderLen)
	if err != nil {
		return sv2TransportDetection{mode: "plaintext", cause: "peek-failed"}
	}
	extType := uint16(hdr[0]) | uint16(hdr[1])<<8
	msgType := hdr[2]
	payloadLen := int(readUint24LE(hdr[3:6]))
	// First plaintext mining message from miner should be SetupConnection on ext=0.
	if extType == 0 && msgType == 0x00 && payloadLen > 0 {
		return sv2TransportDetection{mode: "plaintext", cause: "setupconnection-header"}
	}
	return sv2TransportDetection{
		mode:  "noise",
		cause: fmt.Sprintf("first-bytes do not match plaintext SetupConnection header (ext=0x%04x msg=0x%02x len=%d)", extType, msgType, payloadLen),
	}
}

func newSV2FrameTransportAuto(r io.Reader, w io.Writer) (sv2FrameTransport, sv2TransportDetection, error) {
	d := detectSV2TransportMode(r)
	switch d.mode {
	case "noise":
		return &sv2NoiseFrameTransportStub{cause: d.cause}, d, errSV2NoiseHandshakeNotImplemented
	default:
		return &sv2PlainFrameTransport{r: r, w: w}, d, nil
	}
}

