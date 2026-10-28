package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ensureDevTLS(dir string, hosts ...string) (certFile string, keyFile string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", err
	}
	certFile = filepath.Join(dir, "dev.crt")
	keyFile = filepath.Join(dir, "dev.key")
	if validDevTLS(certFile, keyFile, hosts...) {
		return certFile, keyFile, nil
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "open-Xdownload", Organization: []string{"open-Xdownload"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			if !ip.IsUnspecified() {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		} else if host != "" && host != "localhost" {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return "", "", err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

func validDevTLS(certFile, keyFile string, hosts ...string) bool {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return false
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if block == nil || keyBlock == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return false
	}
	certPublic, ok := cert.PublicKey.(*ecdsa.PublicKey)
	key, okKey := privateKey.(*ecdsa.PrivateKey)
	if !ok || !okKey || certPublic == nil || key == nil || certPublic.X.Cmp(key.PublicKey.X) != 0 || certPublic.Y.Cmp(key.PublicKey.Y) != 0 {
		return false
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter.Add(-24*time.Hour)) {
		return false
	}
	for _, host := range append([]string{"localhost", "127.0.0.1", "::1"}, hosts...) {
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			continue
		}
		if cert.VerifyHostname(host) != nil {
			return false
		}
	}
	return true
}

func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "443"
	}
	return port
}

func envEnabled(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func altSvcHeader(port string) string {
	return fmt.Sprintf(`h3=":%s"; ma=86400`, port)
}
