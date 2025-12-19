package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultDataDir = "data"
	banFileName    = "bans.json"
)

// WorkerView is the subset of worker data that is exposed to the status UI.
type WorkerView struct {
	Name                string       `json:"name"`
	DisplayName         string       `json:"display_name"`
	WorkerSHA256        string       `json:"worker_sha256,omitempty"`
	Accepted            uint64       `json:"accepted"`
	Rejected            uint64       `json:"rejected"`
	BalanceSats         int64        `json:"balance_sats"`
	WalletAddress       string       `json:"wallet_address,omitempty"`
	WalletScript        string       `json:"wallet_script,omitempty"`
	MinerType           string       `json:"miner_type,omitempty"`
	MinerName           string       `json:"miner_name,omitempty"`
	MinerVersion        string       `json:"miner_version,omitempty"`
	LastShare           time.Time    `json:"last_share"`
	LastShareHash       string       `json:"last_share_hash,omitempty"`
	DisplayLastShare    string       `json:"display_last_share,omitempty"`
	LastShareAccepted   bool         `json:"last_share_accepted,omitempty"`
	LastShareDifficulty float64      `json:"last_share_difficulty,omitempty"`
	LastShareDetail     *ShareDetail `json:"last_share_detail,omitempty"`
	Difficulty          float64      `json:"difficulty"`
	Vardiff             float64      `json:"-"`
	RollingHashrate     float64      `json:"rolling_hashrate"`
	LastReject          string       `json:"last_reject"`
	Banned              bool         `json:"banned"`
	BannedUntil         time.Time    `json:"banned_until,omitempty"`
	BanReason           string       `json:"ban_reason,omitempty"`
	WindowStart         time.Time    `json:"window_start"`
	WindowAccepted      int          `json:"window_accepted"`
	WindowSubmissions   int          `json:"window_submissions"`
	ShareRate           float64      `json:"share_rate"`
	ConnectionID        string       `json:"connection_id,omitempty"`
	WalletValidated     bool         `json:"wallet_validated,omitempty"`
}

// RecentWorkView is a minimal view of worker data for the overview page's
// "Recent work" table. It only includes fields that are actually displayed
// to avoid exposing unnecessary worker information.
type RecentWorkView struct {
	Name            string  `json:"name"`
	DisplayName     string  `json:"display_name"`
	RollingHashrate float64 `json:"rolling_hashrate"`
	Difficulty      float64 `json:"difficulty"`
	Vardiff         float64 `json:"vardiff"`
	ShareRate       float64 `json:"share_rate"`
	Accepted        uint64  `json:"accepted"`
	ConnectionID    string  `json:"connection_id"`
}

// WorkerDatabaseStats summarizes high-level worker database metrics exposed
// via status and diagnostics views.
type WorkerDatabaseStats struct {
	TotalWorkers    int
	ActiveWorkers   int
	EstimatedSizeKB int64
}

// ShareDetail holds detailed data for each share, including coinbase transaction details.
type ShareDetail struct {
	Header         string   `json:"header,omitempty"`
	ShareHash      string   `json:"share_hash,omitempty"`
	Target         string   `json:"target,omitempty"`
	Coinbase       string   `json:"coinbase,omitempty"`
	MerkleBranches []string `json:"merkle_branches,omitempty"`
	MerkleRootBE   string   `json:"merkle_root_be,omitempty"`
	MerkleRootLE   string   `json:"merkle_root_le,omitempty"`
	// Decoded coinbase transaction fields for easier inspection.
	CoinbaseVersion    int32                 `json:"coinbase_version,omitempty"`
	CoinbaseLockTime   uint32                `json:"coinbase_locktime,omitempty"`
	CoinbaseHeight     int64                 `json:"coinbase_height,omitempty"`
	CoinbaseOutputs    []CoinbaseOutputDebug `json:"coinbase_outputs,omitempty"`
	TotalCoinbaseValue int64                 `json:"total_coinbase_value,omitempty"`
	BlockSubsidy       int64                 `json:"block_subsidy,omitempty"`
	TransactionFees    int64                 `json:"transaction_fees,omitempty"`
	// Aggregated payout split (computed at view time on the worker page).
	WorkerValueSats   int64   `json:"worker_value_sats,omitempty"`
	PoolValueSats     int64   `json:"pool_value_sats,omitempty"`
	DonationValueSats int64   `json:"donation_value_sats,omitempty"`
	WorkerPercent     float64 `json:"worker_percent,omitempty"`
	PoolPercent       float64 `json:"pool_percent,omitempty"`
	DonationPercent   float64 `json:"donation_percent,omitempty"`
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
	if op < 1 || op > 4 || 1+op > len(script) {
		return 0
	}
	numBytes := op
	var height int64
	for i := 0; i < numBytes; i++ {
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
	for inIdx := uint64(0); inIdx < vinCount; inIdx++ {
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
	var totalValue int64
	for outIdx := uint64(0); outIdx < voutCount; outIdx++ {
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
			ScriptHex: strings.ToLower(hex.EncodeToString(script)),
			Address:   addr,
		})
	}

	if segwit {
		for inIdx := uint64(0); inIdx < vinCount; inIdx++ {
			itemCount, p3, ok := decodeVarInt(raw, pos)
			if !ok {
				return
			}
			pos = p3
			for item := uint64(0); item < itemCount; item++ {
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
	ban   *banList
	ready bool
	err   error
}

// banList holds the persisted bans and synchronizes access via an RWMutex.
type banList struct {
	mu       sync.RWMutex
	entries  map[string]banEntry
	path     string
	writeMu  sync.Mutex
	appendCh chan appendRequest
}

type appendRequest struct {
	entry banEntry
	done  chan error
}

func newBanList(path string) (*banList, error) {
	bl := &banList{
		entries: make(map[string]banEntry),
		path:    path,
	}
	if err := bl.load(); err != nil {
		return nil, err
	}
	if path != "" {
		bl.appendCh = make(chan appendRequest, 32)
		go bl.appendWorker()
	}
	return bl, nil
}

func (b *banList) load() error {
	if b.path == "" {
		return nil
	}
	data, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	entries, err := decodeBanEntries(data)
	if err != nil {
		return err
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	hasActiveBans := false
	for _, entry := range entries {
		if entry.Worker == "" {
			continue
		}
		if entry.Until.IsZero() || now.Before(entry.Until) {
			b.entries[entry.Worker] = entry
			hasActiveBans = true
		}
	}

	if !hasActiveBans && len(entries) > 0 {
		_ = os.Remove(b.path)
	}

	return nil
}

func (b *banList) appendWorker() {
	if b.appendCh == nil {
		return
	}
	for req := range b.appendCh {
		if req.entry.Worker == "" {
			if req.done != nil {
				req.done <- nil
				close(req.done)
			}
			continue
		}
		err := b.appendBanLocked(req.entry)
		if err != nil {
			logger.Warn("append ban entry", "error", err)
		}
		if req.done != nil {
			req.done <- err
			close(req.done)
		}
	}
}

func (b *banList) markBan(worker string, until time.Time, reason string) error {
	if b == nil || b.path == "" || worker == "" {
		return nil
	}
	b.mu.Lock()
	if until.IsZero() {
		delete(b.entries, worker)
		// When unbanning, we need to rewrite the file
		err := b.persistLocked()
		b.mu.Unlock()
		return err
	}

	// Add to in-memory map
	entry := banEntry{
		Worker: worker,
		Until:  until,
		Reason: reason,
	}
	b.entries[worker] = entry
	ch := b.appendCh
	b.mu.Unlock()

	if ch != nil {
		req := appendRequest{
			entry: entry,
			done:  make(chan error, 1),
		}
		ch <- req
		return <-req.done
	}
	return b.appendBanLocked(entry)
}

func (b *banList) appendBanLocked(entry banEntry) error {
	if b.path == "" {
		return nil
	}

	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if info.Size() > 0 {
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}

	data, err := encodeBanEntry(entry)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return nil
}

func (b *banList) persist() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.persistLocked()
}

func (b *banList) persistLocked() error {
	if b.path == "" {
		return nil
	}
	now := time.Now()
	entries := make([]banEntry, 0, len(b.entries))
	for k, entry := range b.entries {
		// Drop expired bans from both the in-memory map and the persisted
		// file so short-lived bans do not accumulate indefinitely.
		if !entry.Until.IsZero() && now.After(entry.Until) {
			delete(b.entries, k)
			continue
		}
		entries = append(entries, entry)
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	if len(entries) == 0 {
		if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove ban file: %w", err)
		}
		return nil
	}
	data, err := encodeBanEntries(entries)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(b.path, data, 0, false); err != nil {
		return err
	}
	return nil
}

func (b *banList) cleanExpired() error {
	if b == nil {
		return nil
	}
	now := time.Now()
	b.mu.Lock()
	removed := false
	for k, entry := range b.entries {
		if entry.Until.IsZero() || now.Before(entry.Until) {
			continue
		}
		delete(b.entries, k)
		removed = true
	}
	b.mu.Unlock()
	if !removed {
		return nil
	}
	return b.persist()
}

func (b *banList) lookup(worker string) (banEntry, bool) {
	if b == nil || worker == "" {
		return banEntry{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.entries[worker]
	if !ok {
		return banEntry{}, false
	}
	if entry.Until.IsZero() || time.Now().After(entry.Until) {
		return banEntry{}, false
	}
	return entry, true
}

func writeFileAtomically(path string, data []byte, bufSize int, syncWrite bool) error {
	tmp := path + ".tmp"
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, bufSize)
	if _, err := w.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if syncWrite {
		if err := f.Sync(); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if syncWrite {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func syncDir(dir string) error {
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer df.Close()
	return df.Sync()
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
	banPath := filepath.Join(stateDir, banFileName)
	banList, err := newBanList(banPath)
	if err != nil {
		return nil, err
	}
	if cleanBans {
		if err := banList.cleanExpired(); err != nil {
			return nil, err
		}
	}
	return &AccountStore{
		ban:   banList,
		ready: true,
	}, nil
}

func (s *AccountStore) WorkerViewByName(workerName string) (WorkerView, bool) {
	if s == nil || workerName == "" || s.ban == nil {
		return WorkerView{}, false
	}
	entry, ok := s.ban.lookup(workerName)
	if !ok {
		return WorkerView{}, false
	}
	return bannedWorkerView(entry), true
}

func (s *AccountStore) WorkerViewBySHA256(sha256Hash string) (WorkerView, bool) {
	if s == nil || sha256Hash == "" || s.ban == nil {
		return WorkerView{}, false
	}
	s.ban.mu.RLock()
	defer s.ban.mu.RUnlock()
	now := time.Now()
	for _, entry := range s.ban.entries {
		if entry.Until.IsZero() || now.After(entry.Until) {
			continue
		}
		if strings.EqualFold(workerNameSHA256(entry.Worker), sha256Hash) {
			return bannedWorkerView(entry), true
		}
	}
	return WorkerView{}, false
}

func bannedWorkerView(entry banEntry) WorkerView {
	display := entry.Worker
	return WorkerView{
		Name:        entry.Worker,
		DisplayName: shortWorkerName(display, workerNamePrefix, workerNameSuffix),
		Banned:      true,
		BannedUntil: entry.Until,
		BanReason:   entry.Reason,
	}
}

func workerNameSHA256(name string) string {
	if name == "" {
		return ""
	}
	sum := sha256Sum([]byte(name))
	return fmt.Sprintf("%x", sum[:])
}

func (s *AccountStore) WorkersSnapshot() []WorkerView {
	if s == nil || s.ban == nil {
		return nil
	}
	s.ban.mu.RLock()
	defer s.ban.mu.RUnlock()
	now := time.Now()
	out := make([]WorkerView, 0, len(s.ban.entries))
	for _, entry := range s.ban.entries {
		if entry.Until.IsZero() || now.After(entry.Until) {
			continue
		}
		out = append(out, bannedWorkerView(entry))
	}
	return out
}

func (s *AccountStore) WorkerDatabaseStats() WorkerDatabaseStats {
	return WorkerDatabaseStats{}
}

func (s *AccountStore) MarkBan(worker string, until time.Time, reason string) {
	if s == nil || s.ban == nil {
		return
	}
	if err := s.ban.markBan(worker, until, reason); err != nil {
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
	if err := s.ban.persist(); err != nil {
		s.err = err
		return err
	}
	return s.LastError()
}
