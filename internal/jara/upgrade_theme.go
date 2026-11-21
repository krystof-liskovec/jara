package jara

// upgradeTheme holds the SGR parameters progress boards paint with, taken
// from Jara's menu palette so bars match the rest of the interface.
type upgradeTheme struct {
	running, done, skipped, failed string
	name, label, muted, track      string
	pulse                          map[string]string // highlight for each state tint
}

// upgradeThemeFor derives progress colors from a menu palette: the accent
// while working, Jara's green when done, and warm yellow and red otherwise.
func upgradeThemeFor(p menuPalette) upgradeTheme {
	yellow, red := "250;189;47", "251;73;52"
	if chatColorIsLight(p.canvas) {
		yellow, red = "181;118;20", "157;0;6"
	}
	fg := func(color string) string { return "38;2;" + color }
	t := upgradeTheme{
		running: fg(p.accent),
		done:    fg(p.green),
		skipped: fg(yellow),
		failed:  fg(red),
		name:    "1;" + fg(p.text),
		label:   fg(p.text),
		muted:   fg(p.muted),
		track:   fg(blendChatColor(p.muted, p.canvas, 0.55)),
	}
	t.pulse = map[string]string{}
	for tint, color := range map[string]string{t.running: p.accent, t.done: p.green, t.skipped: yellow, t.failed: red} {
		t.pulse[tint] = "1;" + fg(blendChatColor(color, p.text, 0.5))
	}
	return t
}

// plainUpgradeTheme uses the terminal's own ANSI colors, for boards that have
// not looked up Jara's palette.
var plainUpgradeTheme = upgradeTheme{
	running: "36", done: "32", skipped: "33", failed: "31",
	name: "1", muted: "2", track: "90",
	pulse: map[string]string{"36": "1;96", "32": "1;92", "33": "1;93", "31": "1;91"},
}
