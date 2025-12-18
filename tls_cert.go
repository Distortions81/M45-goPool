package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ensureSelfSignedCert makes sure a certificate/key pair exists at the given
// paths. If both files already exist, it leaves them untouched. Otherwise it
// generates a new self-signed certificate suitable for local HTTPS/TLS and
// writes it to disk.
func ensureSelfSignedCert(certPath, keyPath string) error {
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("cert or key path empty")
	}
	// Fast path: both files already exist.
	if _, err := os.Stat(certPath); err == nil {
		if _, err2 := os.Stat(keyPath); err2 == nil {
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("mkdir cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return fmt.Errorf("mkdir key dir: %w", err)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate rsa key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}

	notBefore := time.Now().Add(-time.Hour)
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: poolSoftwareName + " self-signed",
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	template.DNSNames = []string{"localhost"}
	template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create cert: %w", err)
	}

	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open cert: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		_ = certOut.Close()
		return fmt.Errorf("write cert: %w", err)
	}
	if err := certOut.Close(); err != nil {
		return fmt.Errorf("close cert: %w", err)
	}

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}); err != nil {
		_ = keyOut.Close()
		return fmt.Errorf("write key: %w", err)
	}
	if err := keyOut.Close(); err != nil {
		return fmt.Errorf("close key: %w", err)
	}

	return nil
}

// certReloader monitors certificate files and automatically reloads them when
// they change. This is essential for certbot renewals where certificates are
// replaced without restarting the server.
type certReloader struct {
	certPath string
	keyPath  string
	certMu   sync.RWMutex
	cert     *tls.Certificate
	lastMod  time.Time
	reloadMu sync.Mutex // Prevents concurrent reload attempts
}

// newCertReloader creates a certificate reloader that monitors the given cert
// and key files. It loads the initial certificate immediately.
func newCertReloader(certPath, keyPath string) (*certReloader, error) {
	cr := &certReloader{
		certPath: certPath,
		keyPath:  keyPath,
	}
	if err := cr.reload(); err != nil {
		return nil, err
	}
	return cr, nil
}

// reload loads the certificate from disk, updating the in-memory copy.
// It uses reloadMu to prevent concurrent reload attempts from multiple
// goroutines (e.g., multiple watch() goroutines or manual reload calls).
func (cr *certReloader) reload() error {
	// Serialize reload attempts to prevent race conditions
	cr.reloadMu.Lock()
	defer cr.reloadMu.Unlock()

	cert, err := tls.LoadX509KeyPair(cr.certPath, cr.keyPath)
	if err != nil {
		return fmt.Errorf("load cert: %w", err)
	}

	// Get modification time of cert file
	info, err := os.Stat(cr.certPath)
	if err != nil {
		return fmt.Errorf("stat cert: %w", err)
	}

	cr.certMu.Lock()
	cr.cert = &cert
	cr.lastMod = info.ModTime()
	cr.certMu.Unlock()

	return nil
}

// getCertificate returns the current certificate. This is compatible with
// tls.Config.GetCertificate.
func (cr *certReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cr.certMu.RLock()
	defer cr.certMu.RUnlock()
	return cr.cert, nil
}

// watch checks the certificate file modification time hourly and reloads if
// changed. This supports certbot automatic renewals.
func (cr *certReloader) watch(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cr.checkAndReload()
		}
	}
}

// checkAndReload checks if the certificate file has been modified and reloads
// if necessary. This is a helper for watch() and can also be called manually.
func (cr *certReloader) checkAndReload() {
	info, err := os.Stat(cr.certPath)
	if err != nil {
		logger.Warn("cert file stat failed during watch", "path", cr.certPath, "error", err)
		return
	}

	cr.certMu.RLock()
	lastMod := cr.lastMod
	cr.certMu.RUnlock()

	if info.ModTime().After(lastMod) {
		logger.Info("certificate file changed, reloading", "path", cr.certPath)
		if err := cr.reload(); err != nil {
			logger.Error("cert reload failed", "error", err)
		} else {
			logger.Info("certificate reloaded successfully")
		}
	}
}
