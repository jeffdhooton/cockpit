package main

import "github.com/jeffdhooton/cockpit/setup"

// spineTemplate documents the optional spine card. It is commented out: the
// default command needs no configuration.
const spineTemplate = `
# [spine]
# command = 'spine tui || spine bearings; exec "$SHELL"'  # what the "spine" session runs
`

// configTemplate is what plain `cockpit init` writes: the minimal config,
// with optional integrations disabled and examples commented out.
func configTemplate() string { return setup.MinimalConfig(nil) + spineTemplate }
