package infra

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/emersion/go-msgauth/dkim"
)

type DKIMConfig struct {
	Enabled        bool
	Domain         string
	Selector       string
	Algorithm      string
	Identity       string
	PrivateKey     string
	PrivateKeyFile string
}

type DKIMSigner struct {
	options dkim.SignOptions
}

func NewDKIMSigner(cfg DKIMConfig) (*DKIMSigner, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	domain := firstLineValue(cfg.Domain)
	selector := firstLineValue(cfg.Selector)
	if domain == "" {
		return nil, fmt.Errorf("dkim domain is required")
	}
	if selector == "" {
		return nil, fmt.Errorf("dkim selector is required")
	}

	keyPEM, err := dkimPrivateKeyPEM(cfg)
	if err != nil {
		return nil, err
	}
	privateKey, keyAlgorithm, err := parseDKIMPrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}
	algorithm := strings.ToLower(strings.TrimSpace(cfg.Algorithm))
	if algorithm == "" {
		algorithm = keyAlgorithm
	}
	if algorithm != keyAlgorithm {
		return nil, fmt.Errorf("dkim private key algorithm mismatch: configured %s, key is %s", algorithm, keyAlgorithm)
	}

	identity := firstLineValue(cfg.Identity)
	if identity == "" {
		identity = "@" + domain
	}
	if !dkimIdentityBelongsToDomain(identity, domain) {
		return nil, fmt.Errorf("dkim identity must belong to signing domain")
	}

	return &DKIMSigner{
		options: dkim.SignOptions{
			Domain:                 domain,
			Selector:               selector,
			Identifier:             identity,
			Signer:                 privateKey,
			Hash:                   crypto.SHA256,
			HeaderCanonicalization: dkim.CanonicalizationRelaxed,
			BodyCanonicalization:   dkim.CanonicalizationRelaxed,
			HeaderKeys: []string{
				"From",
				"To",
				"Subject",
				"Date",
				"Message-ID",
				"MIME-Version",
				"Content-Type",
			},
		},
	}, nil
}

func (s *DKIMSigner) Sign(raw []byte) ([]byte, error) {
	if s == nil {
		return raw, nil
	}

	var signed bytes.Buffer
	if err := dkim.Sign(&signed, bytes.NewReader(raw), &s.options); err != nil {
		return nil, err
	}
	return signed.Bytes(), nil
}

func dkimPrivateKeyPEM(cfg DKIMConfig) ([]byte, error) {
	hasRawKey := strings.TrimSpace(cfg.PrivateKey) != ""
	hasKeyFile := strings.TrimSpace(cfg.PrivateKeyFile) != ""
	if hasRawKey && hasKeyFile {
		return nil, fmt.Errorf("dkim private key must be configured by either raw value or file, not both")
	}
	if hasRawKey {
		return []byte(cfg.PrivateKey), nil
	}
	if !hasKeyFile {
		return nil, fmt.Errorf("dkim private key is required")
	}
	keyPEM, err := os.ReadFile(firstLineValue(cfg.PrivateKeyFile))
	if err != nil {
		return nil, fmt.Errorf("read dkim private key file: %w", err)
	}
	return keyPEM, nil
}

func dkimIdentityBelongsToDomain(identity, domain string) bool {
	identity = strings.ToLower(strings.TrimSuffix(firstLineValue(identity), "."))
	domain = strings.ToLower(strings.TrimSuffix(firstLineValue(domain), "."))
	if identity == "" || domain == "" {
		return false
	}
	at := strings.LastIndex(identity, "@")
	if at < 0 {
		return false
	}
	identityDomain := identity[at+1:]
	return identityDomain == domain || strings.HasSuffix(identityDomain, "."+domain)
}

func parseDKIMPrivateKey(keyPEM []byte) (crypto.Signer, string, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, "", fmt.Errorf("dkim private key must be PEM encoded")
	}

	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("parse dkim private key: %w", err)
	}

	switch privateKey := key.(type) {
	case ed25519.PrivateKey:
		return privateKey, "ed25519-sha256", nil
	case *rsa.PrivateKey:
		if privateKey.N.BitLen() < 2048 {
			return nil, "", fmt.Errorf("dkim rsa private key must be at least 2048 bits")
		}
		return privateKey, "rsa-sha256", nil
	default:
		return nil, "", fmt.Errorf("unsupported dkim private key type %T", key)
	}
}
