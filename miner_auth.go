package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

func normalizeMinerTypeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (mc *MinerConn) minerTypeBanned(minerType, minerName string) bool {
	if mc == nil || len(mc.cfg.BannedMinerTypes) == 0 {
		return false
	}
	typeNorm := normalizeMinerTypeName(minerType)
	nameNorm := normalizeMinerTypeName(minerName)
	if typeNorm == "" && nameNorm == "" {
		return false
	}
	for _, banned := range mc.cfg.BannedMinerTypes {
		bannedNorm := normalizeMinerTypeName(banned)
		if bannedNorm == "" {
			continue
		}
		if bannedNorm == typeNorm || (nameNorm != "" && bannedNorm == nameNorm) {
			return true
		}
	}
	return false
}

// Handle mining.subscribe request.
// Very minimal: return fake subscription and extranonce1/size per docs/protocols/stratum-v1.mediawiki.
func (mc *MinerConn) handleSubscribe(req *StratumRequest) {
	clientID := ""
	haveClientID := false
	// Many miners send a client identifier as the first subscribe parameter.
	// Capture it so we can summarize miner types on the status page.
	if len(req.Params) > 0 {
		if id, ok := req.Params[0].(string); ok {
			clientID = id
			haveClientID = true
		}
	}
	mc.handleSubscribeID(req.ID, clientID, haveClientID)
}

func (mc *MinerConn) handleSubscribeID(id any, clientID string, haveClientID bool) {
	// Ignore duplicate subscribe requests - should only subscribe once
	if mc.subscribed {
		logger.Warn("subscribe rejected: already subscribed", "remote", mc.id)
		mc.writeResponse(StratumResponse{
			ID:     id,
			Result: nil,
			Error:  newStratumError(20, "already subscribed"),
		})
		return
	}

	if haveClientID {
		// Validate client ID length to prevent abuse
		if len(clientID) > maxMinerClientIDLen {
			logger.Warn("subscribe rejected: client identifier too long", "remote", mc.id, "len", len(clientID))
			mc.writeResponse(StratumResponse{
				ID:     id,
				Result: nil,
				Error:  newStratumError(20, "client identifier too long"),
			})
			mc.Close("client identifier too long")
			return
		}
		if clientID != "" {
			// Best-effort split into name/version for nicer aggregation.
			name, ver := parseMinerID(clientID)
			mc.stateMu.Lock()
			mc.minerType = clientID
			if name != "" {
				mc.minerClientName = name
			}
			if ver != "" {
				mc.minerClientVersion = ver
			}
			mc.stateMu.Unlock()
			if mc.minerTypeBanned(clientID, name) {
				logger.Warn("subscribe rejected: banned miner type",
					"remote", mc.id,
					"miner_type", clientID,
					"miner_name", name,
				)
				mc.writeResponse(StratumResponse{
					ID:     id,
					Result: nil,
					Error:  newStratumError(20, "banned miner type"),
				})
				mc.Close("banned miner type")
				return
			}
		}
	}

	mc.subscribed = true

	// Result spec (simplified):
	// [
	//   [ ["mining.set_difficulty", "1"], ["mining.notify", "1"] ],
	//   "extranonce1",
	//   extranonce2_size
	// ]
	ex1 := mc.extranonce1Hex
	en2Size := mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}

	mc.writeSubscribeResponse(id, ex1, en2Size)

	initialJob := mc.jobMgr.CurrentJob()
	if initialJob != nil {
		mc.updateVersionMask(initialJob.VersionMask)
	}
	// Only send mining.set_extranonce if the miner has explicitly subscribed
	// to extranonce notifications via mining.extranonce.subscribe. Sending it
	// unsolicited can confuse miners that don't expect it (e.g., NMAxe/Bitaxe)
	// since the message arrives while they're still sending authorize/configure
	// requests and expecting responses to those.
	if mc.extranonceSubscribed {
		mc.sendSetExtranonce(ex1, en2Size)
	}
	if initialJob == nil {
		status := mc.jobMgr.FeedStatus()
		fields := []any{"remote", mc.id, "reason", "no job available"}
		if status.LastError != nil {
			fields = append(fields, "job_error", status.LastError.Error())
		}
		if !status.LastSuccess.IsZero() {
			fields = append(fields, "last_job_at", status.LastSuccess)
		}
		logger.Warn("miner subscribed but no job ready", fields...)
	}
}

// Handle mining.authorize.
func (mc *MinerConn) handleAuthorize(req *StratumRequest) {
	worker := ""
	pass := ""
	if len(req.Params) > 0 {
		if w, ok := req.Params[0].(string); ok {
			worker = w
		}
	}
	if len(req.Params) > 1 {
		if p, ok := req.Params[1].(string); ok {
			pass = p
		}
	}
	mc.handleAuthorizeID(req.ID, worker, pass)
}

func (mc *MinerConn) handleAuthorizeID(id any, workerParam string, pass string) {
	if !mc.subscribed {
		logger.Warn("authorize rejected: not subscribed", "remote", mc.id)
		mc.writeResponse(StratumResponse{
			ID:     id,
			Result: false,
			Error:  newStratumError(20, "subscribe required"),
		})
		return
	}

	worker := strings.TrimSpace(workerParam)

	// Validate worker name length to prevent abuse
	if len(worker) == 0 {
		logger.Warn("authorize rejected: empty worker name", "remote", mc.id)
		mc.writeResponse(StratumResponse{
			ID:     id,
			Result: false,
			Error:  newStratumError(20, "worker name required"),
		})
		mc.Close("empty worker name")
		return
	}
	if len(worker) > maxWorkerNameLen {
		logger.Warn("authorize rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.writeResponse(StratumResponse{
			ID:     id,
			Result: false,
			Error:  newStratumError(20, "worker name too long"),
		})
		mc.Close("worker name too long")
		return
	}

	if mc.cfg.StratumPasswordEnabled {
		if !authorizePasswordMatches(pass, mc.cfg.StratumPassword) {
			logger.Warn("authorize rejected: invalid stratum password", "remote", mc.id)
			mc.writeResponse(StratumResponse{
				ID:     id,
				Result: false,
				Error:  newStratumError(24, "invalid password"),
			})
			mc.Close("invalid stratum password")
			return
		}
	}

	if bannedView, banned := mc.lookupPersistedBan(worker); banned {
		reason := strings.TrimSpace(bannedView.BanReason)
		if reason == "" {
			reason = "banned"
		}
		mc.stateMu.Lock()
		mc.banUntil = bannedView.BannedUntil
		mc.banReason = reason
		mc.stateMu.Unlock()
		logger.Warn("authorize rejected: worker banned",
			"remote", mc.id,
			"worker", worker,
			"reason", reason,
			"ban_until", bannedView.BannedUntil,
		)
		mc.writeResponse(StratumResponse{
			ID:     id,
			Result: false,
			Error:  newStratumError(24, "banned"),
		})
		mc.Close("banned worker")
		return
	}

	workerName := mc.updateWorker(worker)

	// Before allowing hashing, ensure the worker name is a valid wallet-style
	// address so we can construct dual-payout coinbases. Invalid workers are
	// rejected immediately.
	if workerName != "" {
		if _, _, ok := mc.ensureWorkerWallet(workerName); !ok {
			addr := workerBaseAddress(workerName)
			if addr == "" {
				addr = "(invalid)"
			}
			logger.Warn("worker has invalid wallet-style name",
				"worker", workerName,
				"addr", addr,
			)
			resp := StratumResponse{
				ID:     id,
				Result: false,
				Error:  newStratumError(20, "wallet worker validation failed"),
			}
			mc.writeResponse(resp)
			mc.Close("wallet validation failed")
			return
		}
		// Assign a connection sequence before registering so the saved-workers
		// dashboard can look up active connections via the worker registry.
		mc.assignConnectionSeq()
		mc.registerWorker(workerName)
	}

	passwordDiff, hasPasswordDiff := parsePasswordDifficultyHint(pass)
	if hasPasswordDiff {
		min := mc.cfg.MinDifficulty
		max := mc.cfg.MaxDifficulty
		if min > 0 && max > 0 && max < min {
			max = min
		}
		outOfRange := (min > 0 && passwordDiff < min) || (max > 0 && passwordDiff > max)
		if outOfRange && mc.cfg.EnforceSuggestedDifficultyLimits {
			reason := fmt.Sprintf("suggested difficulty %.8g outside pool limits", passwordDiff)
			if min > 0 && passwordDiff < min {
				reason = "Miner too slow"
			} else if max > 0 && passwordDiff > max {
				reason = "Miner too fast"
			}
			mc.banFor(reason, time.Hour, workerName)
			mc.writeResponse(StratumResponse{
				ID:     id,
				Result: false,
				Error:  newStratumError(24, "banned"),
			})
			mc.Close(reason)
			return
		}
	}

	// Force difficulty to the configured min on authorize so new connections
	// always start at the lowest target we allow.

	mc.authorized = true

	mc.writeTrueResponse(id)

	if !mc.listenerOn {
		// Drain any buffered notifications that may have accumulated between
		// subscribe and authorize; we'll send the current job explicitly below.
		for {
			select {
			case <-mc.jobCh:
			default:
				goto drained
			}
		}
	drained:

		mc.listenerOn = true
		// Goroutine lifecycle: listenJobs reads from mc.jobCh until the channel is closed.
		// Channel is closed via mc.jobMgr.Unsubscribe(mc.jobCh) in cleanup().
		go mc.listenJobs()
	}

	if hasPasswordDiff {
		mc.applySuggestedDifficulty(passwordDiff)
	}

	// Now that the worker is authorized and its wallet-style ID is known
	// to be valid, schedule initial difficulty and a job so hashing can start.
	// We delay very briefly to give miners a chance to send suggest_* first.
	mc.scheduleInitialWork()
	if profiler := getMinerProfileCollector(); profiler != nil {
		profiler.ObserveAuthorize(mc, workerName)
	}
}

func (mc *MinerConn) suggestDifficulty(req *StratumRequest) {
	resp := StratumResponse{ID: req.ID}
	if len(req.Params) == 0 {
		// Some miners send suggest_difficulty with no params to indicate they
		// have no preference. Acknowledge and ignore.
		resp.Result = true
		mc.writeResponse(resp)
		return
	}

	diff, ok := parseSuggestedDifficulty(req.Params[0])
	if !ok || diff < 0 {
		resp.Error = newStratumError(20, "invalid params")
		mc.writeResponse(resp)
		return
	}
	if diff == 0 {
		// Treat 0 as "no preference" (do not lock/adjust, do not ban).
		resp.Result = true
		mc.writeResponse(resp)
		return
	}

	min := mc.cfg.MinDifficulty
	max := mc.cfg.MaxDifficulty
	if min > 0 && max > 0 && max < min {
		max = min
	}
	outOfRange := (min > 0 && diff < min) || (max > 0 && diff > max)
	if outOfRange && mc.cfg.EnforceSuggestedDifficultyLimits {
		worker := mc.currentWorker()
		reason := fmt.Sprintf("suggested difficulty %.8g outside pool limits", diff)
		if min > 0 && diff < min {
			reason = "Miner too slow"
		} else if max > 0 && diff > max {
			reason = "Miner too fast"
		}
		mc.banFor(reason, time.Hour, worker)
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(24, "banned"),
		})
		mc.Close(reason)
		return
	}

	// Always acknowledge the request
	resp.Result = true
	mc.writeResponse(resp)

	// Only process the first mining.suggest_difficulty during initialization.
	// Subsequent requests (from miner keepalive/reconnection) are ignored to
	// prevent disrupting vardiff adjustments and grace period windows.
	mc.applySuggestedDifficulty(diff)
}

func parseSuggestedDifficulty(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case string:
		f, ok := parseSuggestedDifficultyString(v)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case jsonNumber:
		f, err := v.Float64()
		if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
		f, ok := parseSuggestedDifficultyString(v.String())
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func parseSuggestedDifficultyString(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return f, true
	}
	if u, err := strconv.ParseUint(s, 0, 64); err == nil {
		f = float64(u)
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
	}
	return 0, false
}

func normalizeOptionKey(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	return k
}

func splitOptionToken(token string) (string, string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", false
	}
	for i := 0; i < len(token); i++ {
		switch token[i] {
		case '=', ':':
			key := strings.TrimSpace(token[:i])
			val := strings.TrimSpace(token[i+1:])
			if key == "" || val == "" {
				return "", "", false
			}
			return key, val, true
		}
	}
	return "", "", false
}

func splitPasswordTokens(pass string) []string {
	return strings.FieldsFunc(pass, func(r rune) bool {
		switch r {
		case ',', ';', '|', '&', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
}

func authorizePasswordMatches(pass, expected string) bool {
	expected = strings.TrimSpace(expected)
	pass = strings.TrimSpace(pass)
	if pass == expected {
		return true
	}
	for _, token := range splitPasswordTokens(pass) {
		if strings.TrimSpace(token) == expected {
			return true
		}
		key, val, ok := splitOptionToken(token)
		if !ok {
			continue
		}
		switch normalizeOptionKey(key) {
		case "p", "pass", "password":
			if strings.TrimSpace(val) == expected {
				return true
			}
		}
	}
	return false
}

func parsePasswordDifficultyHint(pass string) (float64, bool) {
	for _, token := range splitPasswordTokens(pass) {
		key, val, ok := splitOptionToken(token)
		if !ok {
			continue
		}
		switch normalizeOptionKey(key) {
		case "d", "diff", "difficulty", "sd", "suggestdiff", "suggestdifficulty":
			diff, ok := parseSuggestedDifficultyString(val)
			if !ok || diff <= 0 {
				return 0, false
			}
			return diff, true
		}
	}
	return 0, false
}

func (mc *MinerConn) applySuggestedDifficulty(diff float64) {
	if mc.suggestDiffProcessed {
		logger.Debug("suggest_difficulty ignored (already processed once)", "remote", mc.id)
		return
	}
	mc.suggestDiffProcessed = true

	// If we just restored a recent difficulty for this worker on a short
	// reconnect, ignore suggested-difficulty overrides and keep the
	// existing difficulty so we don't fight the remembered setting.
	if mc.restoredRecentDiff {
		return
	}

	if mc.cfg.LockSuggestedDifficulty {
		// Lock this miner to the requested difficulty (within min/max).
		mc.lockDifficulty = true
	}
	mc.setDifficulty(mc.startupPrimedDifficulty(diff))
	mc.maybeSendInitialWork()
	mc.maybeSendCleanJobAfterSuggest()
}

func parseConfigureExtensions(value any) ([]string, bool) {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, true
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s := strings.TrimSpace(item)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, false
		}
		if strings.Contains(s, ",") {
			parts := strings.Split(s, ",")
			out := make([]string, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					out = append(out, part)
				}
			}
			return out, len(out) > 0
		}
		return []string{s}, true
	default:
		return nil, false
	}
}

func parseConfigureOptions(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		return v
	case map[any]any:
		out := make(map[string]any, len(v))
		for key, val := range v {
			ks, ok := key.(string)
			if !ok {
				continue
			}
			out[ks] = val
		}
		return out
	default:
		return nil
	}
}

func optionValueByAliases(opts map[string]any, aliases ...string) (any, bool) {
	if len(opts) == 0 {
		return nil, false
	}
	for _, alias := range aliases {
		if v, ok := opts[alias]; ok {
			return v, true
		}
	}
	for key, value := range opts {
		keyNorm := normalizeOptionKey(strings.ReplaceAll(key, ".", ""))
		for _, alias := range aliases {
			aliasNorm := normalizeOptionKey(strings.ReplaceAll(alias, ".", ""))
			if keyNorm == aliasNorm {
				return value, true
			}
		}
	}
	return nil, false
}

func parseUint32Hexish(value any) (uint32, bool) {
	switch v := value.(type) {
	case string:
		s := strings.TrimSpace(v)
		s = strings.TrimPrefix(s, "0x")
		s = strings.TrimPrefix(s, "0X")
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return 0, false
		}
		return uint32(n), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > math.MaxUint32 {
			return 0, false
		}
		return uint32(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint32(v), true
	case int64:
		if v < 0 || v > math.MaxUint32 {
			return 0, false
		}
		return uint32(v), true
	case uint32:
		return v, true
	case uint64:
		if v > math.MaxUint32 {
			return 0, false
		}
		return uint32(v), true
	case jsonNumber:
		if n, err := strconv.ParseUint(v.String(), 0, 32); err == nil {
			return uint32(n), true
		}
		if f, ok := parseSuggestedDifficultyString(v.String()); ok && f >= 0 && f <= math.MaxUint32 {
			return uint32(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func parsePositiveInt(value any) (int, bool) {
	diff, ok := parseSuggestedDifficulty(value)
	if !ok || diff <= 0 || diff > float64(math.MaxInt) {
		return 0, false
	}
	return int(diff), true
}

func (mc *MinerConn) suggestTarget(req *StratumRequest) {
	resp := StratumResponse{ID: req.ID}
	if len(req.Params) == 0 {
		// Some miners send suggest_target with no params to indicate they have
		// no preference. Acknowledge and ignore.
		resp.Result = true
		mc.writeResponse(resp)
		return
	}

	targetHex, ok := req.Params[0].(string)
	if !ok || targetHex == "" {
		// Treat empty/unspecified target as "no preference".
		resp.Result = true
		mc.writeResponse(resp)
		return
	}

	diff, ok := difficultyFromTargetHex(targetHex)
	if !ok || diff < 0 {
		resp.Error = newStratumError(20, "invalid target")
		mc.writeResponse(resp)
		return
	}
	if diff == 0 {
		// Treat 0 as "no preference" (do not lock/adjust, do not ban).
		resp.Result = true
		mc.writeResponse(resp)
		return
	}

	min := mc.cfg.MinDifficulty
	max := mc.cfg.MaxDifficulty
	if min > 0 && max > 0 && max < min {
		max = min
	}
	outOfRange := (min > 0 && diff < min) || (max > 0 && diff > max)
	if outOfRange && mc.cfg.EnforceSuggestedDifficultyLimits {
		worker := mc.currentWorker()
		reason := fmt.Sprintf("suggested difficulty %.8g outside pool limits", diff)
		if min > 0 && diff < min {
			reason = "Miner too slow"
		} else if max > 0 && diff > max {
			reason = "Miner too fast"
		}
		mc.banFor(reason, time.Hour, worker)
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(24, "banned"),
		})
		mc.Close(reason)
		return
	}

	// Always acknowledge the request
	resp.Result = true
	mc.writeResponse(resp)

	// Only process the first suggest_target during initialization (same as suggest_difficulty).
	mc.applySuggestedDifficulty(diff)
}

// maybeSendCleanJobAfterSuggest sends a clean notify if initial work was already sent.
// This ensures a fresh job immediately follows a difficulty suggestion.
func (mc *MinerConn) maybeSendCleanJobAfterSuggest() {
	mc.initWorkMu.Lock()
	alreadySent := mc.initialWorkSent
	mc.initWorkMu.Unlock()
	if !alreadySent {
		return
	}
	if !mc.authorized || !mc.listenerOn {
		return
	}
	if job := mc.jobMgr.CurrentJob(); job != nil {
		mc.sendNotifyFor(job, true)
	}
}

// difficultyFromTargetHex converts a target hex string to difficulty.
// difficulty = diff1Target / target
func difficultyFromTargetHex(targetHex string) (float64, bool) {
	// Remove 0x prefix if present
	targetHex = strings.TrimPrefix(targetHex, "0x")
	targetHex = strings.TrimPrefix(targetHex, "0X")

	target, ok := new(big.Int).SetString(targetHex, 16)
	if !ok || target.Sign() <= 0 {
		return 0, false
	}

	// diff = diff1Target / target
	diff1 := new(big.Float).SetInt(diff1Target)
	tgt := new(big.Float).SetInt(target)
	result := new(big.Float).Quo(diff1, tgt)

	diff, _ := result.Float64()
	if diff <= 0 || math.IsInf(diff, 0) || math.IsNaN(diff) {
		return 0, false
	}
	return diff, true
}

func (mc *MinerConn) handleConfigure(req *StratumRequest) {
	if len(req.Params) == 0 {
		mc.writeResponse(StratumResponse{ID: req.ID, Result: nil, Error: newStratumError(20, "invalid params")})
		return
	}

	rawExts, ok := parseConfigureExtensions(req.Params[0])
	if !ok {
		mc.writeResponse(StratumResponse{ID: req.ID, Result: nil, Error: newStratumError(20, "invalid params")})
		return
	}
	var opts map[string]any
	if len(req.Params) > 1 {
		opts = parseConfigureOptions(req.Params[1])
	}

	result := make(map[string]any)
	shouldSendVersionMask := false
	for _, ext := range rawExts {
		name := strings.TrimSpace(ext)
		switch normalizeOptionKey(name) {
		case "versionrolling":
			// BIP310 version-rolling negotiation (docs/protocols/bip-0310.mediawiki).
			if mc.poolMask == 0 {
				result["version-rolling"] = false
				break
			}
			requestMask := mc.poolMask
			if opts != nil {
				if rawMask, found := optionValueByAliases(opts,
					"version-rolling.mask",
					"version_rolling.mask",
					"version-rolling-mask",
					"version_rolling_mask",
				); found {
					if parsed, ok := parseUint32Hexish(rawMask); ok {
						requestMask = parsed
					}
				}
				if rawMinBits, found := optionValueByAliases(opts,
					"version-rolling.min-bit-count",
					"version_rolling.min_bit_count",
					"version-rolling-min-bit-count",
					"version_rolling_min_bit_count",
				); found {
					if minBits, ok := parsePositiveInt(rawMinBits); ok {
						mc.minVerBits = minBits
					}
				}
			}
			mask := requestMask & mc.poolMask
			if mask == 0 {
				result["version-rolling"] = false
				mc.versionRoll = false
				mc.minerMask = requestMask
				mc.updateVersionMask(mc.poolMask)
				break
			}
			available := bits.OnesCount32(mask)
			if mc.minVerBits <= 0 {
				mc.minVerBits = 1
			}
			if mc.minVerBits > available {
				mc.minVerBits = available
			}
			mc.minerMask = requestMask
			mc.versionRoll = true
			mc.versionMask = mask
			result["version-rolling"] = true
			result["version-rolling.mask"] = uint32ToHex8Lower(mask)
			result["version-rolling.min-bit-count"] = mc.minVerBits
			// Important: some miners (including some cgminer-based firmwares)
			// expect the immediate next line after mining.configure to be its
			// JSON-RPC response. If we send an unsolicited notification before
			// the response, they may treat configure as failed and reconnect.
			shouldSendVersionMask = true
		case "suggestdifficulty":
			// Non-standard extension some miners use to confirm support for
			// mining.suggest_difficulty before sending it.
			result[name] = true
		default:
			// Unknown extension; explicitly deny so miners don't retry forever.
			result[name] = false
		}
	}

	mc.writeResponse(StratumResponse{ID: req.ID, Result: result, Error: nil})
	if shouldSendVersionMask {
		mc.sendVersionMask()
	}
}

func (mc *MinerConn) sendNotifyFor(job *Job, forceClean bool) {
	// Opportunistically adjust difficulty before notifying about the job.
	// If difficulty changed, force clean so the miner uses the new difficulty.
	if mc.maybeAdjustDifficulty(time.Now()) {
		forceClean = true
	}

	maskChanged := mc.updateVersionMask(job.VersionMask)
	if maskChanged && mc.versionRoll {
		mc.sendVersionMask()
	}

	// Generate unique scriptTime for this send to prevent duplicate work.
	// Each notification produces a different coinbase, ensuring miners can't
	// produce duplicate shares even if they restart their nonce search.
	mc.jobMu.Lock()
	mc.notifySeq++
	seq := mc.notifySeq
	if mc.jobScriptTime == nil {
		mc.jobScriptTime = make(map[string]int64, mc.maxRecentJobs)
	}
	uniqueScriptTime := job.ScriptTime + int64(seq)
	mc.jobScriptTime[job.JobID] = uniqueScriptTime
	mc.jobMu.Unlock()

	worker := mc.currentWorker()
	if worker == "" {
		logger.Warn("notify aborted: missing authorized worker", "remote", mc.id)
		mc.Close("missing authorized worker")
		return
	}
	if _, _, ok := mc.ensureWorkerWallet(worker); !ok {
		logger.Warn("notify aborted: unable to resolve worker wallet", "remote", mc.id, "worker", worker)
		mc.Close("wallet resolution failed")
		return
	}
	var (
		coinb1 string
		coinb2 string
		err    error
	)
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, worker); ok {
		logger.Debug("payout check", "donation_percent", job.OperatorDonationPercent, "donation_script_len", len(job.DonationScript))
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			logger.Info("using triple payout", "worker", worker, "donation_percent", job.OperatorDonationPercent)
			coinb1, coinb2, err = buildTriplePayoutCoinbaseParts(
				job.Template.Height,
				mc.extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				uniqueScriptTime,
			)
		} else {
			coinb1, coinb2, err = buildDualPayoutCoinbaseParts(
				job.Template.Height,
				mc.extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				uniqueScriptTime,
			)
		}
	}
	// Fallback to single-output coinbase if any required dual-payout parameter is missing.
	if coinb1 == "" || coinb2 == "" || err != nil {
		if err != nil {
			logger.Warn("dual-payout coinbase build failed, falling back to single-output coinbase",
				"error", err,
				"worker", worker,
			)
		}
		coinb1, coinb2, err = buildCoinbaseParts(
			job.Template.Height,
			mc.extranonce1,
			job.Extranonce2Size,
			job.TemplateExtraNonce2Size,
			mc.singlePayoutScript(job, worker),
			job.CoinbaseValue,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			uniqueScriptTime,
		)
	}
	if err != nil {
		logger.Error("notify coinbase parts", "error", err)
		return
	}
	mc.jobMu.Lock()
	if mc.jobNotifyCoinbase == nil {
		mc.jobNotifyCoinbase = make(map[string]notifiedCoinbaseParts, mc.maxRecentJobs)
	}
	mc.jobNotifyCoinbase[job.JobID] = notifiedCoinbaseParts{coinb1: coinb1, coinb2: coinb2}
	mc.jobMu.Unlock()

	prevhashLE := hexToLEHex(job.PrevHash)
	shareTarget := mc.shareTargetOrDefault()

	// clean_jobs should only be true when the template actually changed (prevhash/height)
	// unless we're forcing a clean notify to pair with a difficulty change.
	cleanJobs := forceClean || (job.Clean && mc.cleanFlagFor(job))
	mc.trackJob(job, cleanJobs)
	mc.setJobDifficulty(job.JobID, mc.currentDifficulty())

	// Stratum notify shape per docs/protocols/stratum-v1.mediawiki:
	// [job_id, prevhash, coinb1, coinb2, merkle_branch[], version, nbits, ntime, clean_jobs].
	// Version, bits and ntime are sent as big-endian hex, matching the usual
	// Stratum pool conventions.
	versionBE := int32ToBEHex(int32(job.Template.Version))
	bitsBE := job.Template.Bits // bits is already a raw hex string, don't reverse it
	ntimeBE := uint32ToBEHex(uint32(job.Template.CurTime))

	params := []any{
		job.JobID,
		prevhashLE,
		coinb1,
		coinb2,
		job.MerkleBranches,
		versionBE,
		bitsBE,
		ntimeBE,
		cleanJobs,
	}

	if debugLogging || verboseLogging {
		merkleRoot := computeMerkleRootBE(coinb1, coinb2, job.MerkleBranches)
		headerHashLE := headerHashFromNotify(prevhashLE, merkleRoot, uint32(job.Template.Version), job.Template.Bits, job.Template.CurTime)
		logger.Info("notify payload",
			"job", job.JobID,
			"prevhash", prevhashLE,
			"coinb1", coinb1,
			"coinb2", coinb2,
			"branches", job.MerkleBranches,
			"version", versionBE,
			"bits", bitsBE,
			"ntime", ntimeBE,
			"clean", cleanJobs,
			"share_target", fmt.Sprintf("%064x", shareTarget),
			"merkle_root_be", hex.EncodeToString(merkleRoot),
			"header_hash_le", hex.EncodeToString(headerHashLE),
		)
	}

	if err := mc.writeJSON(map[string]any{
		"id":     nil,
		"method": "mining.notify",
		"params": params,
	}); err != nil {
		logger.Error("notify write error", "remote", mc.id, "error", err)
		return
	}
	mc.recordNotifySent(time.Now())
}

// computeMerkleRootBE rebuilds the merkle root (big-endian) from coinb1/coinb2 and branches.
func computeMerkleRootBE(coinb1, coinb2 string, branches []string) []byte {
	c1, _ := hex.DecodeString(coinb1)
	c2, _ := hex.DecodeString(coinb2)
	cb := append(c1, c2...)
	txid := doubleSHA256(cb)
	return computeMerkleRootFromBranches(txid, branches)
}

// headerHashFromNotify rebuilds the block header hash (LE) from notify fields.
func headerHashFromNotify(prevhash string, merkleRoot []byte, version uint32, bits string, ntime int64) []byte {
	prev, err := hex.DecodeString(prevhash)
	if err != nil || len(prev) != 32 || len(merkleRoot) != 32 {
		return nil
	}
	bitsVal, err := strconv.ParseUint(bits, 16, 32)
	if err != nil {
		return nil
	}
	var hdr bytes.Buffer
	writeUint32LE(&hdr, version)
	hdr.Write(reverseBytes(prev))
	hdr.Write(reverseBytes(merkleRoot))
	writeUint32LE(&hdr, uint32(ntime))
	writeUint32LE(&hdr, uint32(bitsVal))
	writeUint32LE(&hdr, 0) // dummy nonce for hash preview
	h := doubleSHA256(hdr.Bytes())
	return reverseBytes(h)
}
