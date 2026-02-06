package main

import (
	"net/http"
	"strings"
)

type WorkerStatusData struct {
	StatusData
	QueriedWorker     string
	QueriedWorkerHash string // SHA256 hash used by the UI refresh logic
	Worker            *WorkerView
	Error             string
	// Current job coinbase is constructed directly from the active stratum job
	// template (coinb1/coinb2) for this worker, without relying on last-share
	// debug data.
	CurrentJobID       string
	CurrentJobHeight   int64
	CurrentJobPrevHash string
	CurrentJobCoinbase *ShareDetail
	// Hex-encoded scriptPubKey for pool payout, donation, and worker wallet so the
	// UI can label coinbase outputs without re-parsing addresses.
	PoolScriptHex     string
	DonationScriptHex string
	WorkerScriptHex   string
	// FiatNote is an optional human-readable summary of approximate
	// fiat values for the worker's pending balance and last coinbase
	// split, computed using the current BTC price when available.
	FiatNote string
	// PrivacyMode controls whether sensitive wallet/hash data is hidden.
	PrivacyMode bool
	// These flags remember whether sensitive values existed before redaction.
	HasWalletAddress    bool
	HasShareHashDetails bool
}

type WorkerWalletSearchData struct {
	StatusData
	QueriedWalletHash string
	Results           []WorkerView
	Error             string
}

type SignInPageData struct {
	StatusData
	ClerkPublishableKey string
	ClerkJSURL          string
	AfterSignInURL      string
	AfterSignUpURL      string
}

// workerPrivacyModeFromRequest parses the "privacy" query parameter and
// returns whether privacy mode should be enabled for a request. Privacy is
// enabled by default (hiding wallet and hash details) unless explicitly
// disabled via values like "off", "0", "false", or "no".
func workerPrivacyModeFromRequest(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("privacy")))
	switch value {
	case "off", "0", "false", "no":
		return false
	case "on", "1", "true", "yes":
		return true
	default:
		return true
	}
}

// ErrorPageData is used for the generic error page which presents HTTP
// errors using the same layout and styling as the rest of the status UI.
type ErrorPageData struct {
	StatusData
	StatusCode int
	Title      string
	Message    string
	Detail     string
	Path       string
}

func setWorkerStatusView(data *WorkerStatusData, wv WorkerView) {
	data.HasWalletAddress = strings.TrimSpace(wv.WalletAddress) != ""
	data.HasShareHashDetails = strings.TrimSpace(wv.LastShareHash) != "" || strings.TrimSpace(wv.DisplayLastShare) != ""
	if data.QueriedWorkerHash == "" && wv.WorkerSHA256 != "" {
		data.QueriedWorkerHash = wv.WorkerSHA256
	}

	workerScriptHex := ""
	if strings.TrimSpace(wv.WalletScript) != "" {
		workerScriptHex = strings.ToLower(strings.TrimSpace(wv.WalletScript))
	}

	if wv.LastShareDetail != nil && wv.LastShareDetail.Coinbase != "" && len(wv.LastShareDetail.CoinbaseOutputs) == 0 {
		dbg := *wv.LastShareDetail
		dbg.DecodeCoinbaseFields()
		wv.LastShareDetail = &dbg
	}

	// Always set script hex values for template matching
	data.WorkerScriptHex = workerScriptHex

	// Privacy mode is handled client-side, so always send real data
	data.Worker = &wv
}
