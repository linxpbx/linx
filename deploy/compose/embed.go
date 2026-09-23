// Package compose embeds the base Compose stack so linx setup can install it.
package compose

import _ "embed"

// File is compose.yaml.
//
//go:embed compose.yaml
var File []byte
