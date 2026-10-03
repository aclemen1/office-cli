package tui

import (
	"strings"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
)

type mdKey struct {
	md   string
	w    int
	dark bool
}

// mdCache keeps rendered Markdown: the panel redraws several times a second
// while an agent works, and rendering is costly.
var mdCache = map[mdKey]string{}

// renderNotes renders the fiche's Markdown for the terminal, at width w, in
// the shades of the terminal's background, without glamour's outer margins.
func renderNotes(md string, w int) string {
	k := mdKey{md, w, darkBackground}
	if s, ok := mdCache[k]; ok {
		return s
	}
	cfg := styles.DarkStyleConfig
	if !darkBackground {
		cfg = styles.LightStyleConfig
	}
	zero := uint(0)
	cfg.Document.Margin = &zero
	cfg.Document.BlockPrefix, cfg.Document.BlockSuffix = "", ""
	// Headings show by their style; the ## markers would only add noise.
	for _, h := range []*gansi.StyleBlock{&cfg.H2, &cfg.H3, &cfg.H4, &cfg.H5, &cfg.H6} {
		h.Prefix = ""
	}
	out := ""
	if r, err := glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(w)); err == nil {
		if s, err := r.Render(md); err == nil {
			out = strings.Trim(s, "\n")
		}
	}
	if out == "" {
		out = renderNotesPlain(md, w)
	}
	if len(mdCache) > 200 {
		mdCache = map[mdKey]string{}
	}
	mdCache[k] = out
	return out
}
