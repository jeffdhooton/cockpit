package main

import "github.com/jhoot/cockpit/cmd"

var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.SetConfigTemplate(func() string { return configTemplate })
	cmd.Execute()
}
