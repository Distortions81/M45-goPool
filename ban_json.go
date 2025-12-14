package main

import (
	"time"

	"github.com/bytedance/sonic"
)

type banEntry struct {
	Worker string    `json:"worker"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

func encodeBanEntries(bans []banEntry) ([]byte, error) {
	return sonic.ConfigDefault.MarshalIndent(bans, "", "  ")
}

func decodeBanEntries(data []byte) ([]banEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var bans []banEntry
	if err := sonic.Unmarshal(data, &bans); err != nil {
		return nil, err
	}
	return bans, nil
}
