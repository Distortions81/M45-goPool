package main

import (
	"bytes"
	"net/http"
	"time"
)

// handleServerInfoPage renders the public server information page with backend status and diagnostics.
func (s *StatusServer) handleServerInfoPage(w http.ResponseWriter, r *http.Request) {
	if err := s.serveCachedHTML(w, "page_server", func() ([]byte, error) {
		start := time.Now()
		data := s.baseTemplateData(start)
		var buf bytes.Buffer
		if err := s.executeTemplate(&buf, "server", data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}); err != nil {
		logger.Error("server info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Server info page error",
			"We couldn't render the server information page.",
			"Template error while rendering the server info view.")
	}
}

// handlePoolInfo renders a pool configuration/limits summary page. It exposes
// only non-secret settings and aggregate stats that are safe to share.
func (s *StatusServer) handlePoolInfo(w http.ResponseWriter, r *http.Request) {
	if err := s.serveCachedHTML(w, "page_pool", func() ([]byte, error) {
		start := time.Now()
		data := s.baseTemplateData(start)
		var buf bytes.Buffer
		if err := s.executeTemplate(&buf, "pool", data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}); err != nil {
		logger.Error("pool info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Pool info error",
			"We couldn't render the pool configuration page.",
			"Template error while rendering the pool info view.")
	}
}

func (s *StatusServer) handleAboutPage(w http.ResponseWriter, r *http.Request) {
	if err := s.serveCachedHTML(w, "page_about", func() ([]byte, error) {
		start := time.Now()
		data := s.baseTemplateData(start)
		var buf bytes.Buffer
		if err := s.executeTemplate(&buf, "about", data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}); err != nil {
		logger.Error("about page template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"About page error",
			"We couldn't render the about page.",
			"Template error while rendering the about page view.")
	}
}

func (s *StatusServer) handleHelpPage(w http.ResponseWriter, r *http.Request) {
	if err := s.serveCachedHTML(w, "page_help", func() ([]byte, error) {
		start := time.Now()
		data := s.baseTemplateData(start)
		var buf bytes.Buffer
		if err := s.executeTemplate(&buf, "help", data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}); err != nil {
		logger.Error("help page template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Solo mining help page error",
			"We couldn't render the solo mining help page.",
			"Template error while rendering the solo mining help page view.")
	}
}
