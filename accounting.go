package main

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultDataDir = "data"
)

// banEntry represents a banned worker with expiration time and reason.
type banEntry struct {
	Worker string    `json:"worker"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason,omitempty"`
}

// WorkerView is the subset of worker data that is exposed to the status UI.
type WorkerView struct {
	Name                      string       `json:"name"`
	DisplayName               string       `json:"display_name"`
	WorkerSHA256              string       `json:"worker_sha256,omitempty"`
	Accepted                  uint64       `json:"accepted"`
	Rejected                  uint64       `json:"rejected"`
	BalanceSats               int64        `json:"balance_sats"`
	WalletAddress             string       `json:"wallet_address,omitempty"`
	WalletScript              string       `json:"wallet_script,omitempty"`
	MinerType                 string       `json:"miner_type,omitempty"`
	MinerName                 string       `json:"miner_name,omitempty"`
	MinerVersion              string       `json:"miner_version,omitempty"`
	LastShare                 time.Time    `json:"last_share"`
	LastShareHash             string       `json:"last_share_hash,omitempty"`
	DisplayLastShare          string       `json:"display_last_share,omitempty"`
	LastShareAccepted         bool         `json:"last_share_accepted,omitempty"`
	LastShareDifficulty       float64      `json:"last_share_difficulty,omitempty"`
	LastShareDetail           *ShareDetail `json:"last_share_detail,omitempty"`
	Difficulty                float64      `json:"difficulty"`
	Vardiff                   float64      `json:"-"`
	RollingHashrate           float64      `json:"rolling_hashrate"`
	LastReject                string       `json:"last_reject"`
	Banned                    bool         `json:"banned"`
	BannedUntil               time.Time    `json:"banned_until"`
	BanReason                 string       `json:"ban_reason,omitempty"`
	WindowStart               time.Time    `json:"window_start"`
	WindowAccepted            int          `json:"window_accepted"`
	WindowSubmissions         int          `json:"window_submissions"`
	WindowDifficulty          float64      `json:"window_difficulty"`
	ShareRate                 float64      `json:"share_rate"`
	HashrateAccuracy          string       `json:"hashrate_accuracy,omitempty"`
	SubmitRTTP50MS            float64      `json:"submit_rtt_p50_ms,omitempty"`
	SubmitRTTP95MS            float64      `json:"submit_rtt_p95_ms,omitempty"`
	NotifyToFirstShareMinMS   float64      `json:"notify_to_first_share_min_ms,omitempty"`
	NotifyToFirstShareMS      float64      `json:"notify_to_first_share_ms,omitempty"`
	NotifyToFirstShareP50MS   float64      `json:"notify_to_first_share_p50_ms,omitempty"`
	NotifyToFirstShareP95MS   float64      `json:"notify_to_first_share_p95_ms,omitempty"`
	NotifyToFirstShareSamples int          `json:"notify_to_first_share_samples,omitempty"`
	EstimatedPingP50MS        float64      `json:"estimated_ping_p50_ms,omitempty"`
	EstimatedPingP95MS        float64      `json:"estimated_ping_p95_ms,omitempty"`
	ConnectionID              string       `json:"connection_id,omitempty"`
	ConnectionSeq             uint64       `json:"connection_seq,omitempty"`
	ConnectedAt               time.Time    `json:"connected_at"`
	WalletValidated           bool         `json:"wallet_validated,omitempty"`
}

// RecentWorkView is a minimal view of worker data for the overview page's
// "Recent work" table. It only includes fields that are actually displayed
// to avoid exposing unnecessary worker information.
type RecentWorkView struct {
	Name             string  `json:"name"`
	DisplayName      string  `json:"display_name"`
	RollingHashrate  float64 `json:"rolling_hashrate"`
	HashrateAccuracy string  `json:"hashrate_accuracy,omitempty"`
	Difficulty       float64 `json:"difficulty"`
	Vardiff          float64 `json:"vardiff"`
	ShareRate        float64 `json:"share_rate"`
	Accepted         uint64  `json:"accepted"`
	ConnectionID     string  `json:"connection_id"`
}

// ShareDetail holds detailed data for each share, including coinbase transaction details.
type ShareDetail struct {
	Coinbase string `json:"coinbase,omitempty"`
	// Decoded coinbase transaction fields for easier inspection.
	CoinbaseVersion    int32                 `json:"coinbase_version,omitempty"`
	CoinbaseLockTime   uint32                `json:"coinbase_locktime,omitempty"`
	CoinbaseHeight     int64                 `json:"coinbase_height,omitempty"`
	CoinbaseOutputs    []CoinbaseOutputDebug `json:"coinbase_outputs,omitempty"`
	TotalCoinbaseValue int64                 `json:"total_coinbase_value,omitempty"`
	BlockSubsidy       int64                 `json:"block_subsidy,omitempty"`
	TransactionFees    int64                 `json:"transaction_fees,omitempty"`
}

// CoinbaseOutputDebug is a minimal decoded view of a coinbase output.
type CoinbaseOutputDebug struct {
	ValueSats int64   `json:"value_sats"`
	ScriptHex string  `json:"script_hex,omitempty"`
	Percent   float64 `json:"percent,omitempty"`
	Address   string  `json:"address,omitempty"`
}

// decodeVarInt parses a Bitcoin-style varint from b starting at pos.
// It returns the value, the new position, and whether parsing succeeded.
func decodeVarInt(b []byte, pos int) (uint64, int, bool) {
	if pos >= len(b) {
		return 0, pos, false
	}
	prefix := b[pos]
	pos++
	switch prefix {
	case 0xfd:
		if pos+2 > len(b) {
			return 0, pos, false
		}
		v := uint64(binary.LittleEndian.Uint16(b[pos : pos+2]))
		return v, pos + 2, true
	case 0xfe:
		if pos+4 > len(b) {
			return 0, pos, false
		}
		v := uint64(binary.LittleEndian.Uint32(b[pos : pos+4]))
		return v, pos + 4, true
	case 0xff:
		if pos+8 > len(b) {
			return 0, pos, false
		}
		v := binary.LittleEndian.Uint64(b[pos : pos+8])
		return v, pos + 8, true
	default:
		return uint64(prefix), pos, true
	}
}

func decodeCoinbaseHeight(script []byte) int64 {
	if len(script) == 0 {
		return 0
	}
	op := int(script[0])
	// Bounds check: ensure we have enough bytes (1+op ensures script[1+i] is safe for i < op)
	if op < 1 || op > 4 || 1+op > len(script) {
		return 0
	}
	numBytes := op
	var height int64
	for i := range numBytes {
		height |= int64(script[1+i]) << (8 * i)
	}
	if height < 0 {
		return 0
	}
	return height
}

// calculateBlockSubsidy returns the block subsidy (base reward) for a given height.
// Bitcoin halves every 210,000 blocks.
func calculateBlockSubsidy(height int64) int64 {
	if height < 0 {
		return 0
	}
	initialSubsidy := int64(50 * 1e8) // 50 BTC in satoshis
	halvings := height / 210000
	if halvings >= 64 {
		return 0 // After 64 halvings, subsidy is 0
	}
	subsidy := initialSubsidy >> uint(halvings)
	return subsidy
}

func (d *ShareDetail) DecodeCoinbaseFields() {
	if d == nil || d.Coinbase == "" {
		return
	}
	raw, err := hex.DecodeString(d.Coinbase)
	if err != nil || len(raw) < 4 {
		return
	}
	pos := 0
	d.CoinbaseVersion = int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += 4

	segwit := false
	if pos+2 <= len(raw) && raw[pos] == 0x00 && raw[pos+1] == 0x01 {
		segwit = true
		pos += 2
	}

	vinCount, pos2, ok := decodeVarInt(raw, pos)
	if !ok || vinCount == 0 {
		return
	}
	pos = pos2

	var height int64
	for inIdx := range vinCount {
		if pos+36 > len(raw) {
			return
		}
		pos += 36
		scriptLen, p, ok := decodeVarInt(raw, pos)
		if !ok {
			return
		}
		pos = p
		if int(scriptLen) < 0 || pos+int(scriptLen) > len(raw) {
			return
		}
		script := raw[pos : pos+int(scriptLen)]
		if inIdx == 0 {
			if h := decodeCoinbaseHeight(script); h > 0 {
				height = h
			}
		}
		pos += int(scriptLen)
		if pos+4 > len(raw) {
			return
		}
		pos += 4
	}
	d.CoinbaseHeight = height

	voutCount, p, ok := decodeVarInt(raw, pos)
	if !ok {
		return
	}
	pos = p
	var outs []CoinbaseOutputDebug
	if voutCount > 0 && voutCount <= 1024 {
		outs = make([]CoinbaseOutputDebug, 0, int(voutCount))
	}
	var totalValue int64
	for range voutCount {
		if pos+8 > len(raw) {
			return
		}
		value := int64(binary.LittleEndian.Uint64(raw[pos : pos+8]))
		totalValue += value
		pos += 8
		scriptLen, p2, ok := decodeVarInt(raw, pos)
		if !ok {
			return
		}
		pos = p2
		if int(scriptLen) < 0 || pos+int(scriptLen) > len(raw) {
			return
		}
		script := raw[pos : pos+int(scriptLen)]
		pos += int(scriptLen)
		addr := scriptToAddress(script, ChainParams())
		outs = append(outs, CoinbaseOutputDebug{
			ValueSats: value,
			ScriptHex: hex.EncodeToString(script),
			Address:   addr,
		})
	}

	if segwit {
		for range vinCount {
			itemCount, p3, ok := decodeVarInt(raw, pos)
			if !ok {
				return
			}
			pos = p3
			for range itemCount {
				itemLen, p4, ok := decodeVarInt(raw, pos)
				if !ok {
					return
				}
				pos = p4
				if int(itemLen) < 0 || pos+int(itemLen) > len(raw) {
					return
				}
				pos += int(itemLen)
			}
		}
	}

	if pos+4 > len(raw) {
		return
	}
	d.CoinbaseLockTime = binary.LittleEndian.Uint32(raw[pos : pos+4])

	// Set total coinbase value
	d.TotalCoinbaseValue = totalValue

	// Calculate block subsidy and transaction fees if we have height
	if height > 0 {
		d.BlockSubsidy = calculateBlockSubsidy(height)
		if totalValue >= d.BlockSubsidy {
			d.TransactionFees = totalValue - d.BlockSubsidy
		}
	}

	if totalValue > 0 {
		for i := range outs {
			outs[i].Percent = (float64(outs[i].ValueSats) * 100) / float64(totalValue)
		}
	}
	d.CoinbaseOutputs = outs
}

// BestShare tracks one of the best shares seen so far.
type BestShare struct {
	Worker     string    `json:"worker"`
	Difficulty float64   `json:"difficulty"`
	Timestamp  time.Time `json:"timestamp"`
	Hash       string    `json:"hash,omitempty"`
	// Display fields are computed for UI presentation and not persisted to disk.
	DisplayWorker string `json:"display_worker,omitempty"`
	DisplayHash   string `json:"display_hash,omitempty"`
}

type AccountStore struct {
	ban   *banStore
	ready bool
	err   error
}

type banStore struct {
	// Uses getSharedStateDB() for all database operations
}

func (b *banStore) cleanExpired(now time.Time) error {
	db := getSharedStateDB()
	if b == nil || db == nil {
		return nil
	}
	_, err := db.Exec("DELETE FROM bans WHERE until_unix != 0 AND until_unix <= ?", now.Unix())
	return err
}

func (b *banStore) markBan(worker string, until time.Time, reason string, now time.Time) error {
	db := getSharedStateDB()
	if b == nil || db == nil {
		return nil
	}
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return nil
	}
	if until.IsZero() {
		_, err := db.Exec("DELETE FROM bans WHERE worker = ?", worker)
		return err
	}
	workerHash := strings.ToLower(strings.TrimSpace(workerNameHash(worker)))
	if workerHash == "" {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO bans (worker, worker_hash, until_unix, reason, updated_at_unix)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(worker) DO UPDATE SET
			worker_hash = excluded.worker_hash,
			until_unix = excluded.until_unix,
			reason = excluded.reason,
			updated_at_unix = excluded.updated_at_unix
	`, worker, workerHash, unixOrZero(until), strings.TrimSpace(reason), now.Unix())
	return err
}

func (b *banStore) lookup(worker string, now time.Time) (banEntry, bool) {
	db := getSharedStateDB()
	if b == nil || db == nil {
		return banEntry{}, false
	}
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return banEntry{}, false
	}
	var (
		untilUnix int64
		reason    sql.NullString
	)
	if err := db.QueryRow("SELECT until_unix, reason FROM bans WHERE worker = ?", worker).Scan(&untilUnix, &reason); err != nil {
		return banEntry{}, false
	}
	if untilUnix != 0 && now.Unix() >= untilUnix {
		return banEntry{}, false
	}
	entry := banEntry{Worker: worker}
	if untilUnix != 0 {
		entry.Until = time.Unix(untilUnix, 0)
	}
	if reason.Valid {
		entry.Reason = strings.TrimSpace(reason.String)
	}
	return entry, true
}

func (b *banStore) lookupByHash(workerHash string, now time.Time) (banEntry, bool) {
	db := getSharedStateDB()
	if b == nil || db == nil {
		return banEntry{}, false
	}
	workerHash = strings.ToLower(strings.TrimSpace(workerHash))
	if workerHash == "" {
		return banEntry{}, false
	}
	var (
		worker    string
		untilUnix int64
		reason    sql.NullString
	)
	if err := db.QueryRow(`
		SELECT worker, until_unix, reason
		FROM bans
		WHERE worker_hash = ? AND (until_unix = 0 OR until_unix > ?)
		LIMIT 1
	`, workerHash, now.Unix()).Scan(&worker, &untilUnix, &reason); err != nil {
		return banEntry{}, false
	}
	entry := banEntry{Worker: strings.TrimSpace(worker)}
	if untilUnix != 0 {
		entry.Until = time.Unix(untilUnix, 0)
	}
	if reason.Valid {
		entry.Reason = strings.TrimSpace(reason.String)
	}
	if entry.Worker == "" {
		return banEntry{}, false
	}
	return entry, true
}

func (b *banStore) snapshot(now time.Time) []banEntry {
	db := getSharedStateDB()
	if b == nil || db == nil {
		return nil
	}
	rows, err := db.Query(`
		SELECT worker, until_unix, reason
		FROM bans
		WHERE until_unix = 0 OR until_unix > ?
	`, now.Unix())
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []banEntry
	for rows.Next() {
		var (
			worker    string
			untilUnix int64
			reason    sql.NullString
		)
		if err := rows.Scan(&worker, &untilUnix, &reason); err != nil {
			continue
		}
		worker = strings.TrimSpace(worker)
		if worker == "" {
			continue
		}
		entry := banEntry{Worker: worker}
		if untilUnix != 0 {
			entry.Until = time.Unix(untilUnix, 0)
		}
		if reason.Valid {
			entry.Reason = strings.TrimSpace(reason.String)
		}
		out = append(out, entry)
	}
	return out
}

func syncFileIfExists(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	return f.Sync()
}

func NewAccountStore(cfg Config, enableShareLog bool, cleanBans bool) (*AccountStore, error) {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}

	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		return nil, os.ErrInvalid
	}
	bans := &banStore{}

	if cleanBans {
		if err := bans.cleanExpired(time.Now()); err != nil {
			return nil, err
		}
	}
	return &AccountStore{
		ban:   bans,
		ready: true,
	}, nil
}

func (s *AccountStore) WorkerViewByName(workerName string) (WorkerView, bool) {
	if s == nil || workerName == "" || s.ban == nil {
		return WorkerView{}, false
	}
	entry, ok := s.ban.lookup(workerName, time.Now())
	if !ok {
		return WorkerView{}, false
	}
	return bannedWorkerView(entry), true
}

func (s *AccountStore) WorkerViewBySHA256(sha256Hash string) (WorkerView, bool) {
	if s == nil || sha256Hash == "" || s.ban == nil {
		return WorkerView{}, false
	}
	entry, ok := s.ban.lookupByHash(sha256Hash, time.Now())
	if !ok {
		return WorkerView{}, false
	}
	return bannedWorkerView(entry), true
}

func bannedWorkerView(entry banEntry) WorkerView {
	display := entry.Worker
	return WorkerView{
		Name:         entry.Worker,
		DisplayName:  shortWorkerName(display, workerNamePrefix, workerNameSuffix),
		WorkerSHA256: workerNameHash(entry.Worker),
		Banned:       true,
		BannedUntil:  entry.Until,
		BanReason:    entry.Reason,
	}
}

func (s *AccountStore) WorkersSnapshot() []WorkerView {
	if s == nil || s.ban == nil {
		return nil
	}
	now := time.Now()
	entries := s.ban.snapshot(now)
	if len(entries) == 0 {
		return nil
	}
	out := make([]WorkerView, 0, len(entries))
	for _, entry := range entries {
		out = append(out, bannedWorkerView(entry))
	}
	return out
}

func (s *AccountStore) MarkBan(worker string, until time.Time, reason string) {
	if s == nil || s.ban == nil {
		return
	}
	if err := s.ban.markBan(worker, until, reason, time.Now()); err != nil {
		s.err = err
	}
}

func (s *AccountStore) LastError() error {
	return s.err
}

func (s *AccountStore) Ready() bool {
	return s.ready
}

func (s *AccountStore) Flush() error {
	if s == nil || s.ban == nil {
		return s.LastError()
	}
	return s.LastError()
}
