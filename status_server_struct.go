package main

import (
	"context"
	"html/template"
	"sync"
	"sync/atomic"
	"time"
)

type StatusServer struct {
	tmpl                *template.Template
	tmplMu              sync.RWMutex
	jobMgr              *JobManager
	metrics             *PoolMetrics
	registry            *MinerRegistry
	workerRegistry      *workerConnectionRegistry
	accounting          *AccountStore
	rpc                 *RPCClient
	cfg                 atomic.Value
	statusPublicURL     atomic.Value
	ctx                 context.Context
	clerk               *ClerkVerifier
	start               time.Time
	workerLookupLimiter *workerLookupRateLimiter
	workerLists         *workerListStore
	lastStatsMu         sync.Mutex
	lastAccepted        uint64
	lastRejected        uint64
	cpuMu               sync.Mutex
	lastCPUProc         uint64
	lastCPUTotal        uint64
	lastCPUUsage        float64
	oneTimeCodeMu       sync.Mutex
	oneTimeCodes        map[string]oneTimeCodeEntry

	statusMu        sync.RWMutex
	cachedStatus    StatusData
	lastStatusBuild time.Time

	nodeInfoMu         sync.Mutex
	nodeInfo           cachedNodeInfo
	nodeInfoRefreshing int32
	peerLookupMu       sync.Mutex
	peerLookupCache    map[string]peerLookupEntry

	priceSvc    *PriceService
	jsonCacheMu sync.RWMutex
	jsonCache   map[string]cachedJSONResponse

	pageCacheMu sync.RWMutex
	pageCache   map[string]cachedHTMLPage

	workerPageMu    sync.RWMutex
	workerPageCache map[string]cachedWorkerPage

	backupSvc *backblazeBackupService

	configPath      string
	adminConfigPath string
	tuningPath      string
	adminSessions   map[string]time.Time
	adminSessionsMu sync.Mutex
	adminLoginMu    sync.Mutex
	adminLoginNext  time.Time
	requestShutdown func()
	staticFiles     *fileServerWithFallback
}

type cachedJSONResponse struct {
	payload   []byte
	updatedAt time.Time
	expiresAt time.Time
}

type cachedHTMLPage struct {
	payload   []byte
	updatedAt time.Time
}

type cachedWorkerPage struct {
	payload   []byte
	updatedAt time.Time
	expiresAt time.Time
}

func (s *StatusServer) Config() Config {
	if s == nil {
		return Config{}
	}
	if v := s.cfg.Load(); v != nil {
		if cfg, ok := v.(Config); ok {
			return cfg
		}
	}
	return Config{}
}

func (s *StatusServer) UpdateConfig(cfg Config) {
	s.cfg.Store(cfg)
	s.storeStatusPublicURL(cfg.StatusPublicURL)
	s.clearPageCache()
}

func (s *StatusServer) SetStaticFileServer(staticFiles *fileServerWithFallback) {
	if s == nil {
		return
	}
	s.staticFiles = staticFiles
}

func (s *StatusServer) SetBackupService(svc *backblazeBackupService) {
	if s == nil {
		return
	}
	s.backupSvc = svc
}

func (s *StatusServer) ReloadStaticFiles() error {
	if s == nil || s.staticFiles == nil {
		return nil
	}
	return s.staticFiles.ReloadCache()
}
