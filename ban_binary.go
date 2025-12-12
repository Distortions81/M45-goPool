package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

const (
	banMagic   = 0x6742414e // "gBAN" in ASCII
	banVersion = 1
)

type banEntry struct {
	Worker string
	Until  time.Time
	Reason string
}

func writeString(w io.Writer, s string) error {
	length := uint32(len(s))
	if err := binary.Write(w, binary.LittleEndian, length); err != nil {
		return err
	}
	_, err := w.Write([]byte(s))
	return err
}

func readString(r io.Reader) (string, error) {
	var length uint32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func encodeBanEntries(bans []banEntry) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, uint32(banMagic)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(banVersion)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(len(bans))); err != nil {
		return nil, err
	}
	for i := range bans {
		entry := bans[i]
		if err := writeString(buf, entry.Worker); err != nil {
			return nil, fmt.Errorf("write worker name %d: %w", i, err)
		}
		until := int64(0)
		if !entry.Until.IsZero() {
			until = entry.Until.Unix()
		}
		if err := binary.Write(buf, binary.LittleEndian, until); err != nil {
			return nil, fmt.Errorf("write ban time %d: %w", i, err)
		}
		if err := writeString(buf, entry.Reason); err != nil {
			return nil, fmt.Errorf("write ban reason %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}

func decodeBanEntries(data []byte) ([]banEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	buf := bytes.NewReader(data)
	var magic, version, count uint32
	if err := binary.Read(buf, binary.LittleEndian, &magic); err != nil {
		return nil, fmt.Errorf("read ban magic: %w", err)
	}
	if magic != banMagic {
		return nil, fmt.Errorf("unexpected ban magic 0x%x", magic)
	}
	if err := binary.Read(buf, binary.LittleEndian, &version); err != nil {
		return nil, fmt.Errorf("read ban version: %w", err)
	}
	if version != banVersion {
		return nil, fmt.Errorf("unsupported ban version %d", version)
	}
	if err := binary.Read(buf, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("read ban count: %w", err)
	}

	bans := make([]banEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		worker, err := readString(buf)
		if err != nil {
			return nil, fmt.Errorf("read worker %d: %w", i, err)
		}
		var unixUntil int64
		if err := binary.Read(buf, binary.LittleEndian, &unixUntil); err != nil {
			return nil, fmt.Errorf("read ban time %d: %w", i, err)
		}
		reason, err := readString(buf)
		if err != nil {
			return nil, fmt.Errorf("read ban reason %d: %w", i, err)
		}
		var until time.Time
		if unixUntil > 0 {
			until = time.Unix(unixUntil, 0)
		}
		bans = append(bans, banEntry{
			Worker: worker,
			Until:  until,
			Reason: reason,
		})
	}
	return bans, nil
}
