package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCertReloaderConcurrentReload verifies that concurrent reload attempts
// don't cause race conditions or duplicate work.
func TestCertReloaderConcurrentReload(t *testing.T) {
	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "test_cert.pem")
	keyPath := filepath.Join(tmpDir, "test_key.pem")

	// Generate initial certificate
	if err := generateTestCert(certPath, keyPath); err != nil {
		t.Fatalf("Failed to generate initial cert: %v", err)
	}

	// Create the cert reloader
	cr, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("Failed to create cert reloader: %v", err)
	}

	// Test concurrent reload attempts
	const numGoroutines = 10
	var wg sync.WaitGroup
	reloadCount := 0
	var countMu sync.Mutex

	for range numGoroutines {
		wg.Go(func() {
			if err := cr.reload(); err != nil {
				t.Errorf("Reload failed: %v", err)
			}
			countMu.Lock()
			reloadCount++
			countMu.Unlock()
		})
	}

	wg.Wait()

	// All goroutines should have completed successfully
	if reloadCount != numGoroutines {
		t.Errorf("Expected %d successful reloads, got %d", numGoroutines, reloadCount)
	}

	// Verify the certificate is still valid
	cert, err := cr.getCertificate(nil)
	if err != nil {
		t.Fatalf("Failed to get certificate after concurrent reloads: %v", err)
	}
	if cert == nil {
		t.Fatal("Certificate is nil after concurrent reloads")
	}
}

// TestCertReloaderModificationDetection verifies that the reloader correctly
// detects when a certificate file has been modified.
func TestCertReloaderModificationDetection(t *testing.T) {
	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "test_cert.pem")
	keyPath := filepath.Join(tmpDir, "test_key.pem")

	// Generate initial certificate
	if err := generateTestCert(certPath, keyPath); err != nil {
		t.Fatalf("Failed to generate initial cert: %v", err)
	}

	// Create the cert reloader
	cr, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("Failed to create cert reloader: %v", err)
	}

	// Get initial modification time
	cr.certMu.RLock()
	initialMod := cr.lastMod
	cr.certMu.RUnlock()

	// Wait a bit to ensure modification time will be different
	time.Sleep(10 * time.Millisecond)

	// Modify the certificate by regenerating it
	if err := generateTestCert(certPath, keyPath); err != nil {
		t.Fatalf("Failed to regenerate cert: %v", err)
	}

	// Force check and reload
	cr.checkAndReload()

	// Verify modification time was updated
	cr.certMu.RLock()
	newMod := cr.lastMod
	cr.certMu.RUnlock()

	if !newMod.After(initialMod) {
		t.Errorf("Expected modification time to be updated, initial=%v, new=%v", initialMod, newMod)
	}
}

// TestCertReloaderWatchContext verifies that the watch goroutine properly
// respects context cancellation.
func TestCertReloaderWatchContext(t *testing.T) {
	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "test_cert.pem")
	keyPath := filepath.Join(tmpDir, "test_key.pem")

	// Generate initial certificate
	if err := generateTestCert(certPath, keyPath); err != nil {
		t.Fatalf("Failed to generate initial cert: %v", err)
	}

	// Create the cert reloader
	cr, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("Failed to create cert reloader: %v", err)
	}

	// Start watch with a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		cr.watch(ctx)
		close(done)
	}()

	// Cancel the context
	cancel()

	// Verify the watch goroutine exits promptly
	select {
	case <-done:
		// Success - watch exited
	case <-time.After(2 * time.Second):
		t.Fatal("watch() goroutine did not exit after context cancellation")
	}
}

// generateTestCert is a helper that generates a test certificate and key pair.
func generateTestCert(certPath, keyPath string) error {
	// Generate private key
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	// Create certificate template
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	notBefore := time.Now().Add(-time.Hour)
	notAfter := notBefore.Add(24 * time.Hour)

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "test-cert",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Create certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	// Write certificate
	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		certOut.Close()
		return err
	}
	if err := certOut.Close(); err != nil {
		return err
	}

	// Write private key
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}); err != nil {
		keyOut.Close()
		return err
	}
	return keyOut.Close()
}
