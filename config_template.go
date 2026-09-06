package main

import "github.com/jeffdhooton/cockpit/setup"

// configTemplate is what plain `cockpit init` writes: the minimal config,
// with optional integrations disabled and examples commented out.
func configTemplate() string { return setup.MinimalConfig(nil) }
