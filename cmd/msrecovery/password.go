package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
)

const (
	generatedPasswordLength = 20

	passwordUppercase = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	passwordLowercase = "abcdefghijklmnopqrstuvwxyz"
	passwordDigits    = "0123456789"
	passwordSymbols   = "!@#$%^&*_-+="
	passwordAlphabet  = passwordUppercase + passwordLowercase + passwordDigits + passwordSymbols
)

// generatePassword returns a cryptographically secure password suitable for a
// Microsoft account. The generated value must remain write-only: callers must
// never include it in logs or error messages.
func generatePassword() (string, error) {
	return generatePasswordWithReader(rand.Reader)
}

// generatePasswordWithReader is generatePassword with an injectable random
// source. It exists so callers and tests can exercise failures without replacing
// crypto/rand.Reader globally.
func generatePasswordWithReader(random io.Reader) (string, error) {
	if random == nil {
		return "", errors.New("generate password: random source is nil")
	}

	password := make([]byte, generatedPasswordLength)
	requiredClasses := [...]string{
		passwordUppercase,
		passwordLowercase,
		passwordDigits,
		passwordSymbols,
	}

	// Seed one character from every required class before filling the remaining
	// positions from the full alphabet. A secure shuffle below removes any class
	// position information.
	for i, class := range requiredClasses {
		char, err := randomPasswordCharacter(random, class)
		if err != nil {
			return "", fmt.Errorf("generate password: select required character: %w", err)
		}
		password[i] = char
	}
	for i := len(requiredClasses); i < len(password); i++ {
		char, err := randomPasswordCharacter(random, passwordAlphabet)
		if err != nil {
			return "", fmt.Errorf("generate password: select character: %w", err)
		}
		password[i] = char
	}

	if err := shufflePassword(random, password); err != nil {
		return "", fmt.Errorf("generate password: shuffle: %w", err)
	}
	return string(password), nil
}

func randomPasswordCharacter(random io.Reader, alphabet string) (byte, error) {
	index, err := randomPasswordIndex(random, len(alphabet))
	if err != nil {
		return 0, err
	}
	return alphabet[index], nil
}

// shufflePassword applies Fisher-Yates using crypto/rand.Int-backed indices.
func shufflePassword(random io.Reader, password []byte) error {
	for i := len(password) - 1; i > 0; i-- {
		j, err := randomPasswordIndex(random, i+1)
		if err != nil {
			return err
		}
		password[i], password[j] = password[j], password[i]
	}
	return nil
}

func randomPasswordIndex(random io.Reader, upperBound int) (int, error) {
	if upperBound <= 0 {
		return 0, errors.New("invalid random upper bound")
	}
	value, err := rand.Int(random, big.NewInt(int64(upperBound)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}
