package main

import "time"

const adminSessionCookieName = "admin_session"

type AdminPageData struct {
	StatusData
	AdminEnabled         bool
	AdminConfigPath      string
	LoggedIn             bool
	AdminLoginError      string
	AdminApplyError      string
	AdminReloadError     string
	AdminPersistError    string
	AdminRebootError     string
	AdminNotice          string
	AdminLoginsLoadError string
	AdminBansLoadError   string
	Settings             AdminSettingsData
	AdminSection         string
	AdminMinerRows       []AdminMinerRow
	AdminSavedWorkerRows []AdminSavedWorkerRow
	AdminBannedWorkers   []WorkerView
	AdminMinerPagination AdminPagination
	AdminLoginPagination AdminPagination
	AdminBansPagination  AdminPagination
	AdminPerPageOptions  []int
}

type AdminSettingsData struct {
	// Branding/UI
	StatusBrandName                 string
	StatusBrandDomain               string
	StatusPublicURL                 string
	StatusTagline                   string
	StatusConnectMinerTitleExtra    string
	StatusConnectMinerTitleExtraURL string
	FiatCurrency                    string
	GitHubURL                       string
	DiscordURL                      string
	ServerLocation                  string
	MempoolAddressURL               string
	PoolDonationAddress             string
	OperatorDonationName            string
	OperatorDonationURL             string
	PayoutAddress                   string
	PoolFeePercent                  float64
	OperatorDonationPercent         float64
	PoolEntropy                     string
	PoolTagPrefix                   string

	// Listeners
	ListenAddr       string
	StatusAddr       string
	StatusTLSAddr    string
	StratumTLSListen string

	// Rate limits
	MaxConns                          int
	MaxAcceptsPerSecond               int
	MaxAcceptBurst                    int
	AutoAcceptRateLimits              bool
	AcceptReconnectWindow             int
	AcceptBurstWindow                 int
	AcceptSteadyStateWindow           int
	AcceptSteadyStateRate             int
	AcceptSteadyStateReconnectPercent float64
	AcceptSteadyStateReconnectWindow  int

	// Timeouts
	ConnectionTimeoutSeconds int

	// Difficulty / mining toggles
	MinDifficulty           float64
	MaxDifficulty           float64
	TargetSharesPerMin      float64
	LockSuggestedDifficulty bool
	SoloMode                bool
	DirectSubmitProcessing  bool
	CheckDuplicateShares    bool

	// Peer cleanup
	PeerCleanupEnabled   bool
	PeerCleanupMaxPingMs float64
	PeerCleanupMinPeers  int

	// Bans
	CleanExpiredBansOnStartup            bool
	BanInvalidSubmissionsAfter           int
	BanInvalidSubmissionsWindowSeconds   int
	BanInvalidSubmissionsDurationSeconds int
	ReconnectBanThreshold                int
	ReconnectBanWindowSeconds            int
	ReconnectBanDurationSeconds          int

	// Logging
	LogLevel string

	// Tuning / misc
	DiscordWorkerNotifyThresholdSeconds int
	HashrateEMATauSeconds               float64
	NTimeForwardSlackSeconds            int
}

type AdminMinerRow struct {
	ConnectionSeq       uint64
	ConnectionLabel     string
	RemoteAddr          string
	Listener            string
	Worker              string
	WorkerHash          string
	ClientName          string
	ClientVersion       string
	Difficulty          float64
	Hashrate            float64
	AcceptRatePerMinute float64
	SubmitRatePerMinute float64
	Stats               MinerStats
	ConnectedAt         time.Time
	LastActivity        time.Time
	LastShare           time.Time
	Banned              bool
	BanReason           string
	BanUntil            time.Time
}

type AdminSavedWorkerRow struct {
	UserID            string
	Workers           []SavedWorkerEntry
	NotifyCount       int
	WorkerHashes      []string
	OnlineConnections []AdminMinerConnection
	FirstSeen         time.Time
	LastSeen          time.Time
	SeenCount         int
}

type AdminMinerConnection struct {
	ConnectionSeq   uint64
	ConnectionLabel string
	RemoteAddr      string
	Listener        string
}

type AdminPagination struct {
	Page        int
	PerPage     int
	TotalItems  int
	TotalPages  int
	RangeStart  int
	RangeEnd    int
	HasPrevPage bool
	HasNextPage bool
	PrevPage    int
	NextPage    int
}

const (
	defaultAdminPerPage = 25
	maxAdminPerPage     = 200
)

var adminPerPageOptions = []int{10, 25, 50, 100}

const (
	adminMinConnsLimit               = 0
	adminMaxConnsLimit               = 1_000_000
	adminMinAcceptsPerSecondLimit    = 0
	adminMaxAcceptsPerSecondLimit    = 100_000
	adminMinAcceptBurstLimit         = 0
	adminMaxAcceptBurstLimit         = 500_000
	adminMinConnectionTimeoutSeconds = 30
	adminMaxConnectionTimeoutSeconds = 86_400
)
