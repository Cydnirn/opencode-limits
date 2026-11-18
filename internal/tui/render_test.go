package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestRenderBar covers the inline bar's fill, clamping and zero-width guard.
// Theme construction goes through NewTheme, so ASCII mode comes from TestMain.
func TestRenderBar(t *testing.T) {
	theme := themeForTest()

	tests := []struct {
		name     string
		fraction float64
		width    int
		want     string
	}{
		{name: "zero width renders nothing", fraction: 0.5, width: 0, want: ""},
		{name: "negative width renders nothing", fraction: 0.5, width: -3, want: ""},
		{name: "empty bar", fraction: 0, width: 6, want: "      "},
		{name: "full bar", fraction: 1, width: 6, want: "======"},
		{name: "half bar", fraction: 0.5, width: 6, want: "===" + "   "},
		{name: "fraction above one clamps", fraction: 4, width: 4, want: "===="},
		{name: "negative fraction clamps to empty", fraction: -2, width: 4, want: "    "},
		{
			name:     "tiny positive fraction still shows a cell",
			fraction: 0.01,
			width:    6,
			want:     "=" + "     ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderBar(theme, tt.fraction, tt.width, 70, 95)
			if got != tt.want {
				t.Errorf("renderBar(%v, %d) = %q, want %q", tt.fraction, tt.width, got, tt.want)
			}
			if tt.width > 0 {
				if w := lipgloss.Width(got); w != tt.width {
					t.Errorf("renderBar(%v, %d) width = %d, want %d", tt.fraction, tt.width, w, tt.width)
				}
			}
		})
	}
}

// TestPadRight covers padding, exact fit, and truncation.
func TestPadRight(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "pads short text", in: "abc", width: 6, want: "abc   "},
		{name: "exact fit unchanged", in: "abc", width: 3, want: "abc"},
		{name: "truncates long text with an ellipsis", in: "abcdef", width: 4, want: "abc…"},
		{name: "empty input pads", in: "", width: 3, want: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := padRight(tt.in, tt.width); got != tt.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

// TestPadLeft covers left padding and the truncation path.
func TestPadLeft(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "pads short text", in: "abc", width: 6, want: "   abc"},
		{name: "exact fit unchanged", in: "abc", width: 3, want: "abc"},
		{name: "truncates overlong text", in: "abcdef", width: 4, want: "abc…"},
		{name: "empty input pads", in: "", width: 3, want: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := padLeft(tt.in, tt.width); got != tt.want {
				t.Errorf("padLeft(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

// TestTruncateDisplay covers the ellipsis logic and the degenerate widths,
// which previously produced a bare ellipsis for width 1.
func TestTruncateDisplay(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "fits unchanged", in: "abc", width: 5, want: "abc"},
		{name: "no width returns empty", in: "abc", width: 0, want: ""},
		{name: "negative width returns empty", in: "abc", width: -1, want: ""},
		{name: "width one is a lone ellipsis", in: "abc", width: 1, want: "…"},
		{name: "cuts to width minus the ellipsis", in: "abcdef", width: 4, want: "abc…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateDisplay(tt.in, tt.width); got != tt.want {
				t.Errorf("truncateDisplay(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

// TestPadCenter covers centring, including the odd-space case where the right
// side gets the extra cell.
func TestPadCenter(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "even padding", in: "ab", width: 6, want: "  ab  "},
		{name: "odd padding favours the right", in: "ab", width: 5, want: " ab  "},
		{name: "exact fit unchanged", in: "abc", width: 3, want: "abc"},
		{name: "overlong truncates", in: "abcdef", width: 4, want: "abc…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := padCenter(tt.in, tt.width)
			if got != tt.want {
				t.Errorf("padCenter(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
			if w := lipgloss.Width(got); w != tt.width {
				t.Errorf("padCenter(%q, %d) width = %d, want %d", tt.in, tt.width, w, tt.width)
			}
		})
	}
}

// TestWrapText covers the wrapper that keeps legends inside the terminal.
func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{name: "no width yields nothing", in: "abc def", width: 0, want: nil},
		{name: "empty input yields nothing", in: "", width: 10, want: nil},
		{name: "fits on one line", in: "abc def", width: 20, want: []string{"abc def"}},
		{name: "wraps at a space", in: "abc def ghi", width: 7, want: []string{"abc def", "ghi"}},
		{name: "word longer than width is kept whole", in: "abcdefghij k", width: 4, want: []string{"abcdefghij", "k"}},
		{name: "collapses repeated spaces", in: "a   b", width: 20, want: []string{"a b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := wrapText(tt.in, tt.width)
			if len(got) != len(tt.want) {
				t.Fatalf("wrapText(%q, %d) = %v, want %v", tt.in, tt.width, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestWriteWrapped covers the indented writer, which every legend now uses.
func TestWriteWrapped(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		indent string
		width  int
		want   string
	}{
		{name: "single line", in: "abc def", indent: "  ", width: 20, want: "  abc def\n"},
		{
			name:   "wraps within the indented budget",
			in:     "abc def ghi",
			indent: "  ",
			width:  9,
			want:   "  abc def\n  ghi\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder
			writeWrapped(&b, tt.in, tt.indent, tt.width)
			if got := b.String(); got != tt.want {
				t.Errorf("writeWrapped(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
				if w := lipgloss.Width(line); w > tt.width {
					t.Errorf("line %q is %d columns, want <= %d", line, w, tt.width)
				}
			}
		})
	}
}

// TestColumnsTotal covers the small layout helper's gap accounting.
func TestColumnsTotal(t *testing.T) {
	tests := []struct {
		name string
		cols columns
		want int
	}{
		{name: "two equal columns with one gap", cols: columns{widths: []int{4, 4}, gap: 2}, want: 10},
		{name: "three columns with gaps", cols: columns{widths: []int{4, 4, 4}, gap: 1}, want: 14},
		{name: "single column has no gap", cols: columns{widths: []int{7}, gap: 3}, want: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cols.total(); got != tt.want {
				t.Errorf("total() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestFitWidths covers the flex-column distributor: it must hit the target
// width, respect minimums, and never return a negative width.
func TestFitWidths(t *testing.T) {
	tests := []struct {
		name     string
		widths   []int
		minWidth []int //nolint:revive // keeps the table readable
		gap      int
		target   int
		flexIdx  int
		want     []int
	}{
		{
			name:    "flex column absorbs the surplus",
			widths:  []int{10, 10},
			gap:     2,
			target:  30,
			flexIdx: 1,
			want:    []int{10, 18},
		},
		{
			name:     "flex column stops at its minimum",
			widths:   []int{10, 10},
			minWidth: []int{0, 8},
			gap:      2,
			target:   15,
			flexIdx:  1,
			want:     []int{5, 8},
		},
		{
			name:    "negative flex index leaves widths alone",
			widths:  []int{10, 10},
			gap:     0,
			target:  5,
			flexIdx: -1,
			want:    []int{10, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fitWidths(tt.widths, tt.minWidth, tt.gap, tt.target, tt.flexIdx)
			if len(got) != len(tt.want) {
				t.Fatalf("fitWidths returned %d widths, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("position %d = %d, want %d (full: %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// TestJoinRow covers cell padding and the zero-width passthrough.
func TestJoinRow(t *testing.T) {
	tests := []struct {
		name   string
		cells  []string
		widths []int
		gap    int
		want   string
	}{
		{name: "pads to the given widths", cells: []string{"a", "b"}, widths: []int{2, 2}, gap: 1, want: "a  b "},
		{name: "zero width passes through", cells: []string{"abc", "d"}, widths: []int{0, 2}, gap: 1, want: "abc d "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := joinRow(tt.cells, tt.widths, tt.gap); got != tt.want {
				t.Errorf("joinRow(%v) = %q, want %q", tt.cells, got, tt.want)
			}
		})
	}
}

// TestStripANSI covers the escape stripper used for width measurement and for
// every content assertion.
func TestStripANSI(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Render("bold")
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text unchanged", in: "plain", want: "plain"},
		{name: "escapes removed", in: styled, want: "bold"},
		{name: "empty stays empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := stripANSI(tt.in); got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestVisibleWidth covers the width helper, which must count cells rather than
// bytes so multi-byte glyphs do not inflate the measurement.
func TestVisibleWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "ascii", in: "abc", want: 3},
		{name: "empty", in: "", want: 0},
		{name: "block glyph counts as one cell", in: "█", want: 1},
		{name: "ansi is ignored", in: lipgloss.NewStyle().Bold(true).Render("abc"), want: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := visibleWidth(tt.in); got != tt.want {
				t.Errorf("visibleWidth(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestThemeGlyphs covers both glyph sets: ASCII must stay within one byte per
// cell or column maths breaks.
func TestThemeGlyphs(t *testing.T) {
	tests := []struct {
		name  string
		ascii bool
		want  rune
		empty rune
	}{
		{name: "unicode uses blocks", ascii: false, want: '█', empty: '░'},
		{name: "ascii uses equals", ascii: true, want: '=', empty: ' '},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := Theme{ASCII: tt.ascii}.Glyphs()
			if g.Full != tt.want || g.Empty != tt.empty {
				t.Errorf("Glyphs() full=%q empty=%q, want %q %q", g.Full, g.Empty, tt.want, tt.empty)
			}
			// Every glyph must occupy exactly one display cell.
			for _, r := range []rune{g.Full, g.Empty} {
				if w := lipgloss.Width(string(r)); w != 1 {
					t.Errorf("glyph %q occupies %d cells, want 1", r, w)
				}
			}
		})
	}
}

// TestToneFor covers the threshold boundaries the meter colours depend on.
func TestToneFor(t *testing.T) {
	theme := themeForTest()
	tests := []struct {
		name    string
		percent float64
		want    lipgloss.Style
	}{
		{name: "below warning is good", percent: 0, want: theme.Good},
		{name: "just below warning is good", percent: 69.9, want: theme.Good},
		{name: "at warning is warn", percent: 70, want: theme.Warn},
		{name: "just below danger is warn", percent: 94.9, want: theme.Warn},
		{name: "at danger is bad", percent: 95, want: theme.Bad},
		{name: "over danger is bad", percent: 120, want: theme.Bad},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := theme.ToneFor(tt.percent, 70, 95); got.Render("x") != tt.want.Render("x") {
				t.Errorf("ToneFor(%v) = %q, want %q", tt.percent, got.Render("x"), tt.want.Render("x"))
			}
		})
	}
}

// TestBarStyle mirrors ToneFor's thresholds on the bar palette.
func TestBarStyle(t *testing.T) {
	theme := themeForTest()
	tests := []struct {
		name    string
		percent float64
		want    lipgloss.Style
	}{
		{name: "below warning fills normally", percent: 10, want: theme.BarFull},
		{name: "at warning uses the warn fill", percent: 70, want: theme.BarWarn},
		{name: "at danger uses the bad fill", percent: 95, want: theme.BarBad},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := theme.BarStyle(tt.percent, 70, 95); got.Render("x") != tt.want.Render("x") {
				t.Errorf("BarStyle(%v) = %q, want %q", tt.percent, got.Render("x"), tt.want.Render("x"))
			}
		})
	}
}

// TestMinMax covers the two helpers the screens lean on for clamps.
func TestMinMax(t *testing.T) {
	tests := []struct {
		name    string
		a, b    int
		wantMin int
		wantMax int
	}{
		{name: "first smaller", a: 1, b: 5, wantMin: 1, wantMax: 5},
		{name: "second smaller", a: 5, b: 1, wantMin: 1, wantMax: 5},
		{name: "equal", a: 3, b: 3, wantMin: 3, wantMax: 3},
		{name: "negative", a: -5, b: 2, wantMin: -5, wantMax: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := min(tt.a, tt.b); got != tt.wantMin {
				t.Errorf("min(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.wantMin)
			}
			if got := max(tt.a, tt.b); got != tt.wantMax {
				t.Errorf("max(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.wantMax)
			}
		})
	}
}
