package infra

import (
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
)

func NewConfiguredSMTPDelivery(cfg platform.SMTPConfig) (mailapp.SenderPort, error) {
	signer, err := NewDKIMSigner(DKIMConfig{
		Enabled: cfg.DKIMEnabled, Domain: cfg.DKIMDomain, Selector: cfg.DKIMSelector,
		Algorithm: cfg.DKIMAlgorithm, Identity: cfg.DKIMIdentity,
		PrivateKey: cfg.DKIMPrivateKey, PrivateKeyFile: cfg.DKIMPrivateKeyFile,
	})
	if err != nil {
		return nil, err
	}
	if cfg.Mode == "relay" {
		return NewSMTPDelivery(SMTPConfig{
			Addr: cfg.Addr, Username: cfg.Username, Password: cfg.Password,
			From: cfg.From, DKIM: signer,
		}), nil
	}
	return NewDirectSMTPDelivery(DirectSMTPConfig{
		From: cfg.From, Domain: cfg.Domain, HELODomain: cfg.HELODomain, DKIM: signer,
	}), nil
}
