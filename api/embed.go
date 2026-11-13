// Package api embeds the OpenAPI document, so a running instance serves the
// exact contract it was built with.
package api

import _ "embed"

// OpenAPI is the OpenAPI 3.1 description of the HTTP API.
//
//go:embed openapi.yaml
var OpenAPI []byte
