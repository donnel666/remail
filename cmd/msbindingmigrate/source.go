package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func selectSource(domain string) error {
	switch domain {
	case "wsu.edu.kg":
		table = originalTable
	case "lclaitech.com":
		table = "microsoft_binding_migration_lclaitech_aishop6"
	default:
		return errors.New("source domain is outside the authorized migration scope")
	}
	sourceDomain = domain
	return nil
}

func bindStateSource(dir string) error {
	path := filepath.Join(dir, "source-domain")
	data, err := os.ReadFile(path)
	if err == nil {
		if strings.TrimSpace(string(data)) != sourceDomain {
			return errors.New("state directory belongs to another source domain")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Existing unlabelled JSON checkpoints belong to the original WSU rollout.
	if sourceDomain != "wsu.edu.kg" {
		checkpoints, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil || len(checkpoints) != 0 {
			return errors.New("a new source domain requires a separate checkpoint directory")
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(sourceDomain + "\n"); err != nil {
		return err
	}
	return f.Sync()
}
