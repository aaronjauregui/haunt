// Package plugins carries haunt's hermes plugins, so the driver can install
// them into a ghost's profile without a checkout beside it.
package plugins

import "embed"

// MemoryProvider is the `haunt` memory provider (hermes/haunt/README.md).
// Only the files hermes loads: its loader imports every .py in the plugin
// directory, and the tests are not part of the plugin.
//
//go:embed hermes/haunt/__init__.py hermes/haunt/plugin.yaml
var MemoryProvider embed.FS
