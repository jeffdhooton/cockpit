// fakeagent stands in for a coding agent in integration tests: it prints a
// line and waits, so a pane running it has a foreground process named for
// whatever the built binary is called.
package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("agent-screen")
	time.Sleep(60 * time.Second)
}
