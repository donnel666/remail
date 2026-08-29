package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const generatedPasswordLength = 16

type pendingChange struct {
	Password string
	Birthday string
}

func prepareAccountChanges(accounts []accountInput, path string, now time.Time) (map[string]pendingChange, error) {
	pending, err := loadPendingChanges(path)
	if err != nil {
		return nil, err
	}
	passwordOwners := make(map[string]string, len(pending))
	for email, change := range pending {
		if owner, exists := passwordOwners[change.Password]; exists && owner != email {
			return nil, errors.New("pending changes contain duplicate passwords")
		}
		passwordOwners[change.Password] = email
	}
	for index := range accounts {
		key := strings.ToLower(accounts[index].Email)
		change, recovering := pending[key]
		if recovering && (change.Password == accounts[index].Password || change.Birthday == accounts[index].BirthdayISO) {
			delete(passwordOwners, change.Password)
			delete(pending, key)
			recovering = false
		}
		if !recovering {
			password, generateErr := generateNewPassword(accounts[index].Password, passwordOwners)
			if generateErr != nil {
				return nil, generateErr
			}
			birthday, generateErr := generateNewBirthday(now, accounts[index].BirthdayISO)
			if generateErr != nil {
				return nil, generateErr
			}
			change = pendingChange{Password: password, Birthday: birthday}
			pending[key] = change
			passwordOwners[password] = key
		}
		accounts[index].NewPassword = change.Password
		accounts[index].NewBirthday = change.Birthday
		accounts[index].Recovering = recovering
	}
	if err := writePendingChanges(path, pending); err != nil {
		return nil, err
	}
	return pending, nil
}

func prunePendingChanges(path string, pending map[string]pendingChange, completed map[string]struct{}) error {
	for email := range completed {
		delete(pending, strings.ToLower(email))
	}
	return writePendingChanges(path, pending)
}

func loadPendingChanges(path string) (map[string]pendingChange, error) {
	result := make(map[string]pendingChange)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open pending changes: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		parts := strings.Split(scanner.Text(), outputSeparator)
		if len(parts) != 3 {
			return nil, fmt.Errorf("pending changes line %d must contain three fields", line)
		}
		email := strings.ToLower(strings.TrimSpace(parts[0]))
		address, parseErr := mail.ParseAddress(email)
		if parseErr != nil || !strings.EqualFold(address.Address, email) {
			return nil, fmt.Errorf("pending changes line %d has an invalid email", line)
		}
		change := pendingChange{Password: strings.TrimSpace(parts[1]), Birthday: strings.TrimSpace(parts[2])}
		if !validGeneratedPassword(change.Password) {
			return nil, fmt.Errorf("pending changes line %d has an invalid password", line)
		}
		birthday, parseErr := normalizeBirthday(change.Birthday)
		if parseErr != nil {
			return nil, fmt.Errorf("pending changes line %d has an invalid birthday", line)
		}
		if _, exists := result[email]; exists {
			return nil, fmt.Errorf("pending changes line %d duplicates an email", line)
		}
		change.Birthday = birthday
		result[email] = change
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read pending changes: %w", err)
	}
	return result, nil
}

func writePendingChanges(path string, pending map[string]pendingChange) error {
	if len(pending) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove pending changes: %w", err)
		}
		return nil
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create pending changes: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect pending changes: %w", err)
	}
	writer := bufio.NewWriter(temporary)
	emails := make([]string, 0, len(pending))
	for email := range pending {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	for _, email := range emails {
		change := pending[email]
		if _, err := fmt.Fprintf(writer, "%s%s%s%s%s\n", email, outputSeparator, change.Password, outputSeparator, change.Birthday); err != nil {
			temporary.Close()
			return fmt.Errorf("write pending changes: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush pending changes: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync pending changes: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close pending changes: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace pending changes: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func generateNewPassword(current string, used map[string]string) (string, error) {
	const uppercase = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const lowercase = "abcdefghijkmnopqrstuvwxyz"
	const digits = "23456789"
	const symbols = "!@#$%"
	const alphabet = uppercase + lowercase + digits + symbols
	for {
		password := make([]byte, generatedPasswordLength)
		categories := []string{uppercase, lowercase, digits, symbols}
		for index, category := range categories {
			value, err := randomCharacter(category)
			if err != nil {
				return "", err
			}
			password[index] = value
		}
		for index := len(categories); index < len(password); index++ {
			value, err := randomCharacter(alphabet)
			if err != nil {
				return "", err
			}
			password[index] = value
		}
		for index := len(password) - 1; index > 0; index-- {
			choice, err := rand.Int(rand.Reader, big.NewInt(int64(index+1)))
			if err != nil {
				return "", fmt.Errorf("shuffle generated password: %w", err)
			}
			other := int(choice.Int64())
			password[index], password[other] = password[other], password[index]
		}
		result := string(password)
		if result != current && validGeneratedPassword(result) {
			if _, exists := used[result]; !exists {
				return result, nil
			}
		}
	}
}

func randomCharacter(alphabet string) (byte, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	if err != nil {
		return 0, fmt.Errorf("generate random character: %w", err)
	}
	return alphabet[index.Int64()], nil
}

func validGeneratedPassword(password string) bool {
	if len(password) != generatedPasswordLength || strings.Contains(password, outputSeparator) || strings.ContainsAny(password, "\r\n \t") {
		return false
	}
	var upper, lower, digit, symbol bool
	for _, char := range password {
		switch {
		case char >= 'A' && char <= 'Z':
			upper = true
		case char >= 'a' && char <= 'z':
			lower = true
		case char >= '0' && char <= '9':
			digit = true
		case strings.ContainsRune("!@#$%", char):
			symbol = true
		default:
			return false
		}
	}
	for index := 2; index < len(password); index++ {
		if password[index] == password[index-1] && password[index] == password[index-2] {
			return false
		}
	}
	return upper && lower && digit && symbol
}

func generateNewBirthday(now time.Time, current string) (string, error) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	todayLocal := now.In(shanghai)
	today := time.Date(todayLocal.Year(), todayLocal.Month(), todayLocal.Day(), 0, 0, 0, 0, time.UTC)
	earliest := today.AddDate(-51, 0, 1)
	latest := today.AddDate(-18, 0, 0)
	days := int64(latest.Sub(earliest)/(24*time.Hour)) + 1
	for {
		offset, err := rand.Int(rand.Reader, big.NewInt(days))
		if err != nil {
			return "", fmt.Errorf("generate random birthday: %w", err)
		}
		birthday := earliest.AddDate(0, 0, int(offset.Int64())).Format("2006-01-02")
		if birthday != current {
			return birthday, nil
		}
	}
}

func birthdayWithinAgeRange(birthday string, now time.Time) bool {
	date, err := time.Parse("2006-01-02", birthday)
	if err != nil {
		return false
	}
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	todayLocal := now.In(shanghai)
	today := time.Date(todayLocal.Year(), todayLocal.Month(), todayLocal.Day(), 0, 0, 0, 0, time.UTC)
	earliest := today.AddDate(-51, 0, 1)
	latest := today.AddDate(-18, 0, 0)
	return !date.Before(earliest) && !date.After(latest)
}
