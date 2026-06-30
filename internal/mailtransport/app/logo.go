package app

import (
	_ "embed"
	"encoding/base64"
)

//go:embed assets/logo.png
var logoPNG []byte

var logoDataURIValue = "data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG)

func logoDataURI() string {
	return logoDataURIValue
}
