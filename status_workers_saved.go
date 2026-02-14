package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

func (s *StatusServer) handleSavedWorkers(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	data := struct {
		StatusData
		OnlineWorkerEntries        []savedWorkerEntry
		OfflineWorkerEntries       []savedWorkerEntry
		SavedWorkersCount          int
		SavedWorkersOnline         int
		SavedWorkersMax            int
		SavedWorkersBestDifficulty float64
		WalletLookupHash           string
		WalletLookupError          string
		WalletLookupResults        []walletLookupResult
		WalletLookupUnsavedCount   int
	}{StatusData: base}
	data.HashrateGraphTitle = "Total Hashrate"
	data.HashrateGraphID = "savedWorkersHashrateChart"
	s.enrichStatusDataWithClerk(r, &data.StatusData)

	if data.ClerkUser == nil {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}

	data.SavedWorkersBestDifficulty = maxSavedWorkerBestDifficulty(data.SavedWorkers)

	data.SavedWorkersMax = maxSavedWorkersPerUser
	data.SavedWorkersCount = len(data.SavedWorkers)
	now := time.Now()

	savedHashes := make(map[string]struct{}, len(data.SavedWorkers))
	savedNames := make(map[string]struct{}, len(data.SavedWorkers))

	perNameRowsShown := make(map[string]int, 16)
	for _, saved := range data.SavedWorkers {
		if hash := strings.ToLower(strings.TrimSpace(saved.Hash)); hash != "" {
			savedHashes[hash] = struct{}{}
		}
		if name := strings.ToLower(strings.TrimSpace(saved.Name)); name != "" {
			savedNames[name] = struct{}{}
		}
		views, lookupHash := s.findSavedWorkerConnections(saved.Name, saved.Hash, now)
		if lookupHash == "" {
			continue
		}

		if len(views) == 0 {
			// Worker is offline
			if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
				continue
			}
			entry := savedWorkerEntry{
				Name:           saved.Name,
				Hash:           lookupHash,
				NotifyEnabled:  saved.NotifyEnabled,
				BestDifficulty: saved.BestDifficulty,
			}
			perNameRowsShown[lookupHash]++
			data.OfflineWorkerEntries = append(data.OfflineWorkerEntries, entry)
		} else {
			// Worker is online, show each connection separately
			for _, view := range views {
				if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
					break
				}
				hashrate := workerHashrateEstimate(view, now)
				duration := now.Sub(view.ConnectedAt)
				if duration < 0 {
					duration = 0
				}
				entry := savedWorkerEntry{
					Name:              saved.Name,
					Hash:              view.WorkerSHA256,
					NotifyEnabled:     saved.NotifyEnabled,
					BestDifficulty:    saved.BestDifficulty,
					Hashrate:          hashrate,
					ShareRate:         view.ShareRate,
					Accepted:          view.Accepted,
					Rejected:          view.Rejected,
					LastShare:         view.LastShare,
					Difficulty:        view.Difficulty,
					EstimatedPingP50MS: view.EstimatedPingP50MS,
					EstimatedPingP95MS: view.EstimatedPingP95MS,
					NotifyToFirstShareMS: view.NotifyToFirstShareMS,
					NotifyToFirstShareP50MS: view.NotifyToFirstShareP50MS,
					NotifyToFirstShareP95MS: view.NotifyToFirstShareP95MS,
					ConnectedDuration: duration,
					ConnectionID:      view.ConnectionID,
					ConnectionSeq:     view.ConnectionSeq,
				}
				data.SavedWorkersOnline++
				perNameRowsShown[lookupHash]++
				data.OnlineWorkerEntries = append(data.OnlineWorkerEntries, entry)
			}
		}
	}

	walletLookupHash, walletLookupErr := parseOrDeriveSHA256(r.URL.Query().Get("hash"), r.URL.Query().Get("wallet"))
	data.WalletLookupHash = walletLookupHash
	if walletLookupHash != "" {
		if walletLookupErr != "" {
			data.WalletLookupError = walletLookupErr
		} else {
			results, errMsg := s.lookupWorkerViewsByWalletHash(walletLookupHash, now)
			if errMsg != "" {
				data.WalletLookupError = errMsg
			} else {
				data.WalletLookupResults = make([]walletLookupResult, 0, len(results))
				for _, view := range results {
					alreadySaved := false
					if viewHash := strings.ToLower(strings.TrimSpace(view.WorkerSHA256)); viewHash != "" {
						if _, ok := savedHashes[viewHash]; ok {
							alreadySaved = true
						}
					}
					if !alreadySaved {
						if nameKey := strings.ToLower(strings.TrimSpace(view.Name)); nameKey != "" {
							if _, ok := savedNames[nameKey]; ok {
								alreadySaved = true
							}
						}
					}
					data.WalletLookupResults = append(data.WalletLookupResults, walletLookupResult{
						WorkerView:   view,
						AlreadySaved: alreadySaved,
					})
					if !alreadySaved {
						data.WalletLookupUnsavedCount++
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.executeTemplate(w, "saved_workers", data); err != nil {
		logger.Error("saved workers template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Saved workers page error",
			"We couldn't render the saved workers page.",
			"Template error while rendering saved workers.")
	}
}

func (s *StatusServer) handleSavedWorkersJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	saved := []SavedWorkerEntry(nil)
	if s.workerLists != nil {
		if list, err := s.workerLists.List(user.UserID); err == nil {
			saved = list
		} else {
			logger.Warn("load saved workers", "error", err, "user_id", user.UserID)
		}
	}
	bestSavedDifficulty := maxSavedWorkerBestDifficulty(saved)

	type entry struct {
		Name                      string  `json:"name"`
		Hash                      string  `json:"hash"`
		Online                    bool    `json:"online"`
		NotifyEnabled             bool    `json:"notify_enabled"`
		BestDifficulty            float64 `json:"best_difficulty"`
		LastOnlineAt              string  `json:"last_online_at,omitempty"`
		LastShare                 string  `json:"last_share,omitempty"`
		Hashrate                  float64 `json:"hashrate"`
		SharesPerMinute           float64 `json:"shares_per_minute"`
		Accepted                  uint64  `json:"accepted"`
		Rejected                  uint64  `json:"rejected"`
		Difficulty                float64 `json:"difficulty"`
		EstimatedPingP50MS        float64 `json:"estimated_ping_p50_ms,omitempty"`
		EstimatedPingP95MS        float64 `json:"estimated_ping_p95_ms,omitempty"`
		NotifyToFirstShareMS      float64 `json:"notify_to_first_share_ms,omitempty"`
		NotifyToFirstShareP50MS   float64 `json:"notify_to_first_share_p50_ms,omitempty"`
		NotifyToFirstShareP95MS   float64 `json:"notify_to_first_share_p95_ms,omitempty"`
		ConnectionSeq             uint64  `json:"connection_seq,omitempty"`
		ConnectionDurationSeconds float64 `json:"connection_duration_seconds,omitempty"`
	}
	now := time.Now()
	discordRegistered := false
	discordUserEnabled := false
	if s.workerLists != nil && discordConfigured(s.Config()) {
		if _, enabled, ok, err := s.workerLists.GetDiscordLink(user.UserID); err == nil {
			discordRegistered = ok
			discordUserEnabled = ok && enabled
		}
	}
	resp := struct {
		UpdatedAt            string  `json:"updated_at"`
		SavedMax             int     `json:"saved_max"`
		SavedCount           int     `json:"saved_count"`
		OnlineCount          int     `json:"online_count"`
		DiscordRegistered    bool    `json:"discord_registered,omitempty"`
		DiscordNotifyEnabled bool    `json:"discord_notify_enabled,omitempty"`
		BestDifficulty       float64 `json:"best_difficulty"`
		OnlineWorkers        []entry `json:"online_workers"`
		OfflineWorkers       []entry `json:"offline_workers"`
	}{
		UpdatedAt:            now.UTC().Format(time.RFC3339),
		SavedMax:             maxSavedWorkersPerUser,
		SavedCount:           len(saved),
		DiscordRegistered:    discordRegistered,
		DiscordNotifyEnabled: discordUserEnabled,
		BestDifficulty:       bestSavedDifficulty,
	}

	perNameRowsShown := make(map[string]int, 16)
	totalRowsSent := 0
	for _, savedEntry := range saved {
		if totalRowsSent >= maxSavedWorkersPerUser {
			break
		}
		views, lookupHash := s.findSavedWorkerConnections(savedEntry.Name, savedEntry.Hash, now)
		if lookupHash == "" {
			continue
		}

		if len(views) == 0 {
			// Worker is offline
			if totalRowsSent >= maxSavedWorkersPerUser {
				break
			}
			if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
				continue
			}
			e := entry{
				Name:           savedEntry.Name,
				Hash:           lookupHash,
				Online:         false,
				NotifyEnabled:  savedEntry.NotifyEnabled,
				BestDifficulty: savedEntry.BestDifficulty,
			}
			perNameRowsShown[lookupHash]++
			totalRowsSent++
			resp.OfflineWorkers = append(resp.OfflineWorkers, e)
		} else {
			// Worker is online, show each connection separately
			for _, view := range views {
				if totalRowsSent >= maxSavedWorkersPerUser {
					break
				}
				if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
					break
				}
				hashrate := workerHashrateEstimate(view, now)
				connectionDurationSeconds := 0.0
				if !view.ConnectedAt.IsZero() {
					connectionDurationSeconds = now.Sub(view.ConnectedAt).Seconds()
					if connectionDurationSeconds < 0 {
						connectionDurationSeconds = 0
					}
				}
				lastShare := ""
				if !view.LastShare.IsZero() {
					lastShare = view.LastShare.UTC().Format(time.RFC3339)
				}
				e := entry{
					Name:                      savedEntry.Name,
					Hash:                      view.WorkerSHA256,
					Online:                    true,
					NotifyEnabled:             savedEntry.NotifyEnabled,
					BestDifficulty:            savedEntry.BestDifficulty,
					Hashrate:                  hashrate,
					SharesPerMinute:           view.ShareRate,
					Accepted:                  view.Accepted,
					Rejected:                  view.Rejected,
					Difficulty:                view.Difficulty,
					EstimatedPingP50MS:        view.EstimatedPingP50MS,
					EstimatedPingP95MS:        view.EstimatedPingP95MS,
					NotifyToFirstShareMS:      view.NotifyToFirstShareMS,
					NotifyToFirstShareP50MS:   view.NotifyToFirstShareP50MS,
					NotifyToFirstShareP95MS:   view.NotifyToFirstShareP95MS,
					LastShare:                 lastShare,
					ConnectionSeq:             view.ConnectionSeq,
					ConnectionDurationSeconds: connectionDurationSeconds,
				}
				perNameRowsShown[lookupHash]++
				resp.OnlineCount++
				totalRowsSent++
				resp.OnlineWorkers = append(resp.OnlineWorkers, e)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		logger.Error("saved workers json marshal", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else if _, err := w.Write(out); err != nil {
		logger.Error("saved workers json write", "error", err)
	}
}

func (s *StatusServer) handleSavedWorkersOneTimeCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !discordConfigured(s.Config()) {
		http.NotFound(w, r)
		return
	}

	now := time.Now()
	code, expiresAt := s.createNewOneTimeCode(user.UserID, now)
	if code == "" || expiresAt.IsZero() {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}{
		Code:      code,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		logger.Error("one time code json marshal", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else if _, err := w.Write(out); err != nil {
		logger.Error("one time code json write", "error", err)
	}
}

func (s *StatusServer) handleSavedWorkersOneTimeCodeClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !discordConfigured(s.Config()) {
		http.NotFound(w, r)
		return
	}

	var code string
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		type req struct {
			Code string `json:"code"`
		}
		var parsed req
		if err := json.NewDecoder(r.Body).Decode(&parsed); err == nil {
			code = strings.TrimSpace(parsed.Code)
		}
	} else {
		_ = r.ParseForm()
		code = strings.TrimSpace(r.FormValue("code"))
	}

	cleared := false
	if code != "" {
		cleared = s.clearOneTimeCode(user.UserID, code, time.Now())
	}

	resp := struct {
		Cleared bool `json:"cleared"`
	}{Cleared: cleared}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		logger.Error("one time code clear json marshal", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else if _, err := w.Write(out); err != nil {
		logger.Error("one time code clear json write", "error", err)
	}
}

func (s *StatusServer) handleSavedWorkersNotifyEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.workerLists == nil {
		http.Error(w, "saved workers not enabled", http.StatusBadRequest)
		return
	}

	type req struct {
		Hash    string `json:"hash"`
		Enabled *bool  `json:"enabled"`
	}
	var parsed req
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&parsed)
	} else {
		_ = r.ParseForm()
		parsed.Hash = r.FormValue("hash")
		if v := strings.TrimSpace(r.FormValue("enabled")); v != "" {
			b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
			parsed.Enabled = &b
		}
	}

	hash := strings.ToLower(strings.TrimSpace(parsed.Hash))
	if hash == "" || len(hash) != 64 {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	if parsed.Enabled == nil {
		http.Error(w, "missing enabled", http.StatusBadRequest)
		return
	}

	list, err := s.workerLists.List(user.UserID)
	if err != nil {
		http.Error(w, "failed to load saved workers", http.StatusInternalServerError)
		return
	}
	found := false
	for _, sw := range list {
		if strings.ToLower(strings.TrimSpace(sw.Hash)) == hash {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	now := time.Now()
	if err := s.workerLists.SetSavedWorkerNotifyEnabled(user.UserID, hash, *parsed.Enabled, now); err != nil {
		logger.Warn("saved worker notify toggle failed", "error", err, "user_id", user.UserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		OK      bool `json:"ok"`
		Enabled bool `json:"enabled"`
	}{
		OK:      true,
		Enabled: *parsed.Enabled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else {
		_, _ = w.Write(out)
	}
}

func (s *StatusServer) handleDiscordNotifyEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.workerLists == nil {
		http.Error(w, "saved workers not enabled", http.StatusBadRequest)
		return
	}
	if !discordConfigured(s.Config()) {
		http.NotFound(w, r)
		return
	}

	type req struct {
		Enabled *bool `json:"enabled"`
	}
	var parsed req
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&parsed)
	} else {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.FormValue("enabled")); v != "" {
			b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
			parsed.Enabled = &b
		}
	}
	if parsed.Enabled == nil {
		http.Error(w, "missing enabled", http.StatusBadRequest)
		return
	}

	now := time.Now()
	ok, err := s.workerLists.SetDiscordLinkEnabled(user.UserID, *parsed.Enabled, now)
	if err != nil {
		logger.Warn("discord notify toggle failed", "error", err, "user_id", user.UserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not registered", http.StatusNotFound)
		return
	}

	resp := struct {
		OK      bool `json:"ok"`
		Enabled bool `json:"enabled"`
	}{
		OK:      true,
		Enabled: *parsed.Enabled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else {
		_, _ = w.Write(out)
	}
}

func (s *StatusServer) handleWorkerSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid submission", http.StatusBadRequest)
		return
	}
	worker := strings.TrimSpace(r.FormValue("worker"))
	if worker == "" {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}
	if s.workerLists != nil {
		if err := s.workerLists.Add(user.UserID, worker); err != nil {
			logger.Warn("save worker name", "error", err, "user_id", user.UserID)
		}
	}
	http.Redirect(w, r, "/saved-workers", http.StatusSeeOther)
}

func (s *StatusServer) handleWorkerRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid submission", http.StatusBadRequest)
		return
	}
	worker := strings.TrimSpace(r.FormValue("worker"))
	if worker == "" {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}
	if s.workerLists != nil {
		if err := s.workerLists.Remove(user.UserID, worker); err != nil {
			logger.Warn("remove worker name", "error", err, "user_id", user.UserID)
		}
	}
	http.Redirect(w, r, "/saved-workers", http.StatusSeeOther)
}
