package main

import "github.com/jeffdhooton/cockpit/cmd"

var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.SetConfigTemplate(configTemplate)
	cmd.Execute()
}
