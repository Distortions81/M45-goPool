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
	AdminLogsLoadError   string
	Settings             AdminSettingsData
	AdminSection         string
	AdminMinerRows       []AdminMinerRow
	AdminSavedWorkerRows []AdminSavedWorkerRow
	AdminBannedWorkers   []WorkerView
	AdminMinerPagination AdminPagination
	AdminLoginPagination AdminPagination
	AdminBansPagination  AdminPagination
	AdminPerPageOptions  []int
	AdminLogSources      []string
	AdminLogSource       string
	OperatorStats        AdminOperatorStatsData
}

type AdminOperatorStatsData struct {
	GeneratedAt time.Time
	Pool        AdminOperatorPoolStats
	Backups     AdminOperatorBackupStats
	Clerk       AdminOperatorClerkStats
	Currency    AdminOperatorCurrencyStats
}

type AdminOperatorPoolStats struct {
	ActiveMiners        int
	PoolHashrate        float64
	SharesPerSecond     float64
	ActiveAdminSessions int
}

type AdminOperatorBackupStats struct {
	Enabled             bool
	B2Enabled           bool
	BucketConfigured    bool
	BucketName          string
	BucketReachable     bool
	Interval            time.Duration
	ForceEveryInterval  bool
	LastAttemptAt       time.Time
	LastSnapshotAt      time.Time
	LastSnapshotVersion int64
	LastUploadAt        time.Time
	LastUploadVersion   int64
	SnapshotPath        string
	LastError           string
}

type AdminOperatorClerkStats struct {
	Configured             bool
	VerifierReady          bool
	Issuer                 string
	JWKSURL                string
	CallbackPath           string
	SignInURL              string
	SessionCookieName      string
	KnownUsers             int
	KnownUsersLoadError    string
	ActiveAdminSessions    int
	LastKeyRefresh         time.Time
	KeyRefreshInterval     time.Duration
	LoadedVerificationKeys int
}

type AdminOperatorCurrencyStats struct {
	FiatCurrency string
	LastPrice    float64
	LastFetchAt  time.Time
	LastError    string
	CacheTTL     time.Duration
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
	DisableConnectRateLimits          bool
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
	MinDifficulty                    float64
	MaxDifficulty                    float64
	DefaultDifficulty                float64
	TargetSharesPerMin               float64
	DifficultyStepGranularity        int
	LockSuggestedDifficulty          bool
	EnforceSuggestedDifficultyLimits bool
	RelaxedSubmitValidation          bool
	SubmitWorkerNameMatch            bool
	DirectSubmitProcessing           bool
	CheckDuplicateShares             bool

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
	StratumMessagesPerMinute            int
	MaxRecentJobs                       int
	Extranonce2Size                     int
	TemplateExtraNonce2Size             int
	JobEntropy                          int
	CoinbaseScriptSigMaxBytes           int
	DiscordWorkerNotifyThresholdSeconds int
	HashrateEMATauSeconds               float64
	NTimeForwardSlackSeconds            int
	MinVersionBits                      int
	IgnoreMinVersionBits                bool
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
	adminMinConnsLimit               = 10
	adminMaxConnsLimit               = 1_000_000
	adminMinAcceptsPerSecondLimit    = 1
	adminMaxAcceptsPerSecondLimit    = 100_000
	adminMinAcceptBurstLimit         = 1
	adminMaxAcceptBurstLimit         = 500_000
	adminMinConnectionTimeoutSeconds = 30
	adminMaxConnectionTimeoutSeconds = 86_400
	adminMinStratumMessagesPerMinute = 10
	adminMaxStratumMessagesPerMinute = 1_000_000
	adminMinMaxRecentJobs            = 8
	adminMaxMaxRecentJobs            = 10_000
	adminMinBanThreshold             = 3
	adminMaxBanThreshold             = 10_000
	adminMinBanWindowSeconds         = 10
	adminMaxBanWindowSeconds         = 86_400
	adminMinBanDurationSeconds       = 10
	adminMaxBanDurationSeconds       = 604_800
	adminMinReconnectBanThreshold    = 3
	adminMaxReconnectBanThreshold    = 10_000
	adminMinReconnectBanWindowSecs   = 10
	adminMaxReconnectBanWindowSecs   = 86_400
	adminMinReconnectBanDurationSecs = 10
	adminMaxReconnectBanDurationSecs = 604_800
	adminMinExtranonce2Size          = 1
	adminMaxExtranonce2Size          = 8
	adminMinTemplateExtranonce2Size  = 1
	adminMaxTemplateExtranonce2Size  = 16
)

const (
	adminMinTargetSharesPerMin = 0.1
	adminMaxTargetSharesPerMin = 120.0
)
