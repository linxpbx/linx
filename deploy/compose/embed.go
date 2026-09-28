// Package compose embeds the Compose stacks so linx setup can install them.
package compose

import _ "embed"

// File is compose.yaml.
//
//go:embed compose.yaml
var File []byte

// InstallFile is install.yaml, the web install's first stack.
//
//go:embed install.yaml
var InstallFile []byte
