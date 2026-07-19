package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCSVSafeNeutralizesSpreadsheetFormulas(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "equals", value: "=HYPERLINK(\"https://example.test\")", want: `"'=HYPERLINK(""https://example.test"")"`},
		{name: "plus", value: "+SUM(1,2)", want: `"'+SUM(1,2)"`},
		{name: "minus", value: "-1+1", want: `"'-1+1"`},
		{name: "at", value: "@SUM(1,2)", want: `"'@SUM(1,2)"`},
		{name: "leading spaces", value: "  =1+1", want: `"'  =1+1"`},
		{name: "leading tab", value: "\tvalue", want: "\"'\tvalue\""},
		{name: "line break before formula", value: "\r\n=1+1", want: `"'  =1+1"`},
		{name: "ordinary email", value: "user@example.test", want: `"user@example.test"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, csvSafe(tt.value))
		})
	}
}
