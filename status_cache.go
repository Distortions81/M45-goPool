package main

import "time"

// invalidateStatusCache clears the status cache, forcing the next request to rebuild.
func (s *StatusServer) invalidateStatusCache() {
	s.statusMu.Lock()
	s.lastStatusBuild = time.Time{}
	s.statusMu.Unlock()
}

// statusDataView returns a read-only snapshot of the cached status data.
//
// Unlike statusData(), it does not deep-clone slices/maps. Callers must treat
// the returned value as immutable (do not mutate slices, maps, or nested
// structs). This is safe for building endpoint-specific responses where we
// copy/censor only the fields we actually serialize.
func (s *StatusServer) statusDataView() StatusData {
	now := time.Now()
	s.statusMu.RLock()
	if !s.lastStatusBuild.IsZero() && now.Sub(s.lastStatusBuild) < overviewRefreshInterval {
		data := s.cachedStatus
		s.statusMu.RUnlock()
		return data
	}
	s.statusMu.RUnlock()

	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if !s.lastStatusBuild.IsZero() && now.Sub(s.lastStatusBuild) < overviewRefreshInterval {
		return s.cachedStatus
	}
	data := s.buildStatusData()
	s.cachedStatus = data
	s.lastStatusBuild = now
	return data
}
