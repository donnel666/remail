package infra

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeExistingAliasRowsUsesMicrosoftWhitelist(t *testing.T) {
	assert.Equal(t, []string{
		"first@outlook.com",
		"second@outlook.com.ar",
	}, normalizeExistingAliasRows([]string{
		" First@Outlook.com ",
		"second@outlook.com.ar",
		"recovery@gmail.com",
		"legacy@live.com",
		"excluded@outlook.co.uk",
		"first@outlook.com",
	}))
}

func TestNormalizeAliasRowsUsesMicrosoftWhitelist(t *testing.T) {
	assert.Equal(t, []string{
		"first@outlook.com",
		"second@hotmail.com",
		"third@outlook.fr",
	}, normalizeAliasRows([]string{
		" First@Outlook.com ",
		"second@hotmail.com",
		"third@outlook.fr",
		"recovery@gmail.com",
	}))
}
