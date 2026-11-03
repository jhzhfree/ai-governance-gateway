// Package openapi contains the checked-in management and mock inference contract.
package openapi

import _ "embed"

//go:embed management.json
var Document []byte
