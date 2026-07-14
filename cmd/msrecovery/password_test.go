package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePasswordProperties(t *testing.T) {
	password, err := generatePasswordWithReader(deterministicPasswordReader())
	require.NoError(t, err)
	require.Len(t, password, generatedPasswordLength)

	assert.True(t, strings.ContainsAny(password, passwordUppercase), "password must contain uppercase ASCII")
	assert.True(t, strings.ContainsAny(password, passwordLowercase), "password must contain lowercase ASCII")
	assert.True(t, strings.ContainsAny(password, passwordDigits), "password must contain a digit")
	assert.True(t, strings.ContainsAny(password, passwordSymbols), "password must contain a safe symbol")

	for _, char := range password {
		assert.Contains(t, passwordAlphabet, string(char), "password contains a character outside the allowed alphabet")
	}
}

func TestGeneratePasswordWithReaderIsDeterministic(t *testing.T) {
	first, err := generatePasswordWithReader(deterministicPasswordReader())
	require.NoError(t, err)
	second, err := generatePasswordWithReader(deterministicPasswordReader())
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestGeneratePasswordUsesFreshRandomness(t *testing.T) {
	const samples = 16
	seen := make(map[string]struct{}, samples)
	for range samples {
		password, err := generatePassword()
		require.NoError(t, err)
		if _, duplicate := seen[password]; duplicate {
			t.Fatalf("generated a duplicate password in %d samples", samples)
		}
		seen[password] = struct{}{}
	}
}

func TestGeneratePasswordWithReaderRejectsNilSource(t *testing.T) {
	password, err := generatePasswordWithReader(nil)

	assert.Empty(t, password)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), passwordAlphabet)
}

func TestGeneratePasswordWithReaderPropagatesRandomSourceError(t *testing.T) {
	wantErr := errors.New("random source unavailable")
	random := io.MultiReader(
		bytes.NewReader(bytes.Repeat([]byte{0x01}, 8)),
		errorReader{err: wantErr},
	)

	password, err := generatePasswordWithReader(random)

	assert.Empty(t, password)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.NotContains(t, err.Error(), passwordAlphabet)
}

func deterministicPasswordReader() io.Reader {
	// crypto/rand.Int consumes a variable number of bytes when it rejects values.
	// This repeating, low-valued stream is accepted for every bound used here and
	// remains long enough for both character selection and Fisher-Yates shuffling.
	pattern := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	return bytes.NewReader(bytes.Repeat(pattern, 32))
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
