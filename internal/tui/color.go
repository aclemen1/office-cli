package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// darkBackground follows the terminal: the TUI asks for its background colour
// at start. Until it answers, colours are those for a dark background.
var darkBackground = true

// paneFocused is false while the focus is in another herdr pane, e.g. the docked agent.
var paneFocused = true

// adaptive is a colour with a shade for each background, chosen at render time.
type adaptive struct{ Light, Dark string }

func (c adaptive) RGBA() (r, g, b, a uint32) {
	if darkBackground {
		return lipgloss.Color(c.Dark).RGBA()
	}
	return lipgloss.Color(c.Light).RGBA()
}

var _ color.Color = adaptive{}
