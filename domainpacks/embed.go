// Package domainpacks embeds the registered domain packs. Only directories
// with a manifest.yaml participate; examples live under docs/domainpacks and
// are never embedded.
package domainpacks

import "embed"

//go:embed */manifest.yaml
var FS embed.FS
