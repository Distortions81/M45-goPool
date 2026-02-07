package main

import (
	"bytes"
	"net/http"
	"time"
)

func (s *StatusServer) SetJobManager(jm *JobManager) {
	s.jobMgr = jm
	// Set up callback to invalidate status cache when new blocks arrive
	jm.onNewBlock = s.invalidateStatusCache
}

func (s *StatusServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/favicon.png":
		http.ServeFile(w, r, "logo.png")

	case r.URL.Path == "/" || r.URL.Path == "":
		if err := s.serveCachedHTML(w, "page_overview", func() ([]byte, error) {
			start := time.Now()
			data := s.baseTemplateData(start)
			var buf bytes.Buffer
			if err := s.executeTemplate(&buf, "overview", data); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}); err != nil {
			logger.Error("status template error", "error", err)
			s.renderErrorPage(w, r, http.StatusInternalServerError,
				"Status page error",
				"We couldn't render the pool status page.",
				"Template error while rendering the main status view.")
		}
	default:
		s.renderErrorPage(w, r, http.StatusNotFound,
			"Page not found",
			"The page you requested could not be found.",
			"Check the URL or use the navigation links above.")
	}
}
