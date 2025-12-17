package main

import (
	"bufio"
	"bytes"
	"time"

	"github.com/bytedance/sonic"
)

type banEntry struct {
	Worker string    `json:"worker"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason,omitempty"`
}

// encodeBanEntry serializes a single ban entry in compact JSON format so we can
// append it without rewriting the whole file.
func encodeBanEntry(entry banEntry) ([]byte, error) {
	return sonic.ConfigDefault.Marshal(entry)
}

// encodeBanEntries serializes multiple entries one per line to match the
// newline-delimited file format.
func encodeBanEntries(entries []banEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	for i, entry := range entries {
		if i > 0 {
			buf.WriteByte('\n')
		}
		data, err := encodeBanEntry(entry)
		if err != nil {
			return nil, err
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

// decodeBanEntries can parse both the old JSON array format and the newer
// newline-delimited format used for append-only writes.
func decodeBanEntries(data []byte) ([]banEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var entries []banEntry
		if err := sonic.Unmarshal(data, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}

	var entries []banEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry banEntry
		if err := sonic.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
