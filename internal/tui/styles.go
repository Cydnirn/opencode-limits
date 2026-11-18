package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds every style the UI uses. Building it once keeps rendering cheap
// and makes the colour/no-colour decision explicit instead of scattered.
type Theme struct {
	Dim       lipgloss.Style
	Bold      lipgloss.Style
	Title     lipgloss.Style
	Subtle    lipgloss.Style
	Header    lipgloss.Style
	Accent    lipgloss.Style
	Good      lipgloss.Style
	Warn      lipgloss.Style
	Bad       lipgloss.Style
	BarFull   lipgloss.Style
	BarEmpty  lipgloss.Style
	BarWarn   lipgloss.Style
	BarBad    lipgloss.Style
	Selected  lipgloss.Style
	TabActive lipgloss.Style
	TabIdle   lipgloss.Style
	Box       lipgloss.Style
	Gauge     lipgloss.Style
	ASCII     bool
}

// NewTheme builds the palette. Colour is disabled when NO_COLOR is set or when
// the terminal does not claim UTF-8, in which case the caller also switches the
// glyph set to ASCII.
func NewTheme() Theme {
	ascii := detectASCII()
	t := Theme{ASCII: ascii}

	if ascii {
		// Rely on weight and structure instead of colour.
		t.Dim = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		t.Bold = lipgloss.NewStyle().Bold(true)
		t.Title = lipgloss.NewStyle().Bold(true)
		t.Subtle = lipgloss.NewStyle()
		t.Header = lipgloss.NewStyle().Bold(true).Underline(true)
		t.Accent = lipgloss.NewStyle().Bold(true)
		t.Good = lipgloss.NewStyle()
		t.Warn = lipgloss.NewStyle().Bold(true)
		t.Bad = lipgloss.NewStyle().Bold(true).Reverse(true)
		t.BarFull = lipgloss.NewStyle()
		t.BarEmpty = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		t.BarWarn = lipgloss.NewStyle().Bold(true)
		t.BarBad = lipgloss.NewStyle().Reverse(true)
		t.Selected = lipgloss.NewStyle().Reverse(true)
		t.TabActive = lipgloss.NewStyle().Bold(true).Underline(true)
		t.TabIdle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		t.Box = lipgloss.NewStyle()
		t.Gauge = lipgloss.NewStyle()
		return t
	}

	t.Dim = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	t.Bold = lipgloss.NewStyle().Bold(true)
	t.Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	t.Subtle = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	t.Header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250"))
	t.Accent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	t.Good = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	t.Warn = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	t.Bad = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	t.BarFull = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	t.BarEmpty = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	t.BarWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	t.BarBad = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	t.Selected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("238"))
	t.TabActive = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")).Underline(true)
	t.TabIdle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	t.Box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
	t.Gauge = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	return t
}

// ToneFor returns the style matching a percentage used, using the same
// thresholds the Console's own meters apply (danger above 95, warning at 70+).
func (t Theme) ToneFor(percent float64, warning, danger int) lipgloss.Style {
	switch {
	case percent >= float64(danger):
		return t.Bad
	case percent >= float64(warning):
		return t.Warn
	default:
		return t.Good
	}
}

// BarStyle returns the bar fill style matching a percentage used.
func (t Theme) BarStyle(percent float64, warning, danger int) lipgloss.Style {
	switch {
	case percent >= float64(danger):
		return t.BarBad
	case percent >= float64(warning):
		return t.BarWarn
	default:
		return t.BarFull
	}
}

// glyphs holds the drawing characters for the active mode.
type glyphs struct {
	Full, Empty rune
	Partial     [8]rune
	Arrow, Dot  string
	Divider     string
	Bullet      string
}

// Glyphs returns the drawing set for the current mode. Braille blocks give a
// smooth bar in UTF-8 terminals; ASCII falls back to a coarse but readable bar.
func (t Theme) Glyphs() glyphs {
	if t.ASCII {
		return glyphs{
			Full: '=', Empty: ' ',
			Partial: [8]rune{'-', '=', '=', '=', '=', '=', '=', '='},
			Arrow:   ">",
			Divider: "|",
			Bullet:  "*",
		}
	}
	return glyphs{
		Full: '█', Empty: '░',
		Partial: [8]rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'},
		Arrow:   "›",
		Divider: "│",
		Bullet:  "•",
	}
}

// detectASCII reports whether the terminal cannot handle UTF-8 box drawing.
func detectASCII() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	if os.Getenv("OPENCODE_LIMITS_ASCII") != "" {
		return true
	}
	lang := os.Getenv("LC_ALL")
	if lang == "" {
		lang = os.Getenv("LC_CTYPE")
	}
	if lang == "" {
		lang = os.Getenv("LANG")
	}
	if lang == "" {
		// Unknown locale: assume a modern UTF-8 terminal rather than guess.
		return false
	}
	l := strings.ToLower(lang)
	return !strings.Contains(l, "utf-8") && !strings.Contains(l, "utf8")
}
