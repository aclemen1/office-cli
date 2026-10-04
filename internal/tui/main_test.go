package tui

import "github.com/aclemen1/office-cli/internal/app"

func init() {
	agentsNow = func() []app.AgentPane { return nil }
	tuiFocused = func() bool { return true }
}
