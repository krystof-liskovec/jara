package jara

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

func itoa(n int) string { return strconv.Itoa(n) }

// networkPainter styles `jara network` with the menus' palette, or not at
// all when the output is not a terminal, NO_COLOR is set or TERM is dumb.
type networkPainter struct {
	color   bool
	palette menuPalette
}

func newNetworkPainter() networkPainter {
	p := networkPainter{color: os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(os.Stdout.Fd()))}
	if p.color {
		p.palette = menuColors()
	}
	return p
}

// paint applies a style: bold, muted, accent, green or title.
func (p networkPainter) paint(style, text string) string {
	if !p.color || text == "" || style == "" {
		return text
	}
	codes := map[string]string{
		"bold":   "\x1b[1m",
		"muted":  menuFG(p.palette.muted),
		"accent": menuFG(p.palette.accent),
		"green":  menuFG(p.palette.green),
		"title":  "\x1b[1m" + menuFG(p.palette.accent),
	}
	return codes[style] + text + "\x1b[0m"
}

// networkCell is one column of a row and its style.
type networkCell struct{ text, style string }

// table aligns rows into columns by their display width, two spaces apart,
// indented under a section title.
func (p networkPainter) table(b *strings.Builder, rows [][]networkCell) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], uniseg.StringWidth(cell.text))
		}
	}
	for _, row := range rows {
		line := "  "
		for i, cell := range row {
			line += p.paint(cell.style, cell.text)
			if i < len(row)-1 {
				line += strings.Repeat(" ", widths[i]-uniseg.StringWidth(cell.text)+2)
			}
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
}

func (p networkPainter) section(b *strings.Builder, title string) {
	b.WriteString("\n  " + p.paint("title", title) + "\n")
	b.WriteString("  " + p.paint("muted", strings.Repeat("─", uniseg.StringWidth(title))) + "\n")
}

// countOf is "1 folder" or "3 folders".
func countOf(n int, one, many string) string { return itoa(n) + " " + plural(n, one, many) }

// folderSummary is "5 folders · 1 running".
func folderSummary(folders, running int) string {
	summary := countOf(folders, "folder", "folders")
	if running > 0 {
		summary += " · " + itoa(running) + " running"
	}
	return summary
}

// renderNetwork lays out the network: hosts, devices, running sessions.
func renderNetwork(view networkView, now time.Time, p networkPainter) string {
	var b strings.Builder
	b.WriteString("\n  " + p.paint("title", "Jara network") + p.paint("muted", "  ·  seen from "+view.Here+" at "+now.Format("15:04")) + "\n")

	p.section(&b, "Hosts")
	if len(view.Hosts) == 0 {
		b.WriteString("  " + p.paint("muted", "None yet. jara setup adds one; jara join CODE joins a network.") + "\n")
	}
	var rows [][]networkCell
	for i, host := range view.Hosts {
		glyph, detail := networkCell{"●", "green"}, networkCell{host.Version, "muted"}
		switch {
		case host.Outdated:
			glyph, detail = networkCell{"◐", "accent"}, networkCell{host.Problem, "accent"}
		case !host.Online:
			glyph, detail = networkCell{"○", "muted"}, networkCell{"unreachable · " + shortHostError(host.Problem), "accent"}
		}
		rows = append(rows, []networkCell{glyph, {itoa(i + 1), "muted"}, {host.Name, "bold"}, {host.Address, ""}, {host.Role, "muted"}, detail})
	}
	p.table(&b, rows)

	p.section(&b, "Devices")
	rows = nil
	for _, d := range view.Devices {
		rows = append(rows, deviceRow(d, now))
	}
	p.table(&b, rows)

	p.section(&b, "Sessions")
	if len(view.Sessions) == 0 {
		b.WriteString("  " + p.paint("muted", "None running.") + "\n")
	}
	rows = nil
	for i, s := range view.Sessions {
		state := networkCell{"idle", "muted"}
		if s.Busy {
			state = networkCell{"working", "accent"}
		}
		windows := ""
		if s.Attached > 0 {
			windows = countOf(s.Attached, "window", "windows")
		}
		rows = append(rows, []networkCell{{itoa(i + 1), "muted"}, {s.Name, "bold"}, {s.where(), ""}, state, {windows, "muted"}})
	}
	p.table(&b, rows)

	b.WriteString("\n  " + p.paint("muted", "jara attach N  ·  jara device open DEVICE FOLDER  ·  jara network invite") + "\n\n")
	return b.String()
}

// deviceRow describes one device in fixed columns: status glyph, name,
// tag, whether it can be called, the hosts it is linked through, details.
func deviceRow(d networkDeviceView, now time.Time) []networkCell {
	name := networkCell{d.Name, "bold"}
	tag := networkCell{"", "muted"}
	switch {
	case d.This:
		tag.text = "this device"
	case d.Joined:
		tag.text = "joined"
	}
	via := networkCell{"", ""}
	if len(d.Via) > 0 {
		via.text = "via " + strings.Join(d.Via, ", ")
	}
	folders := networkCell{folderSummary(d.Folders, d.Running), "muted"}
	if d.This {
		switch {
		case !d.Callable:
			return []networkCell{{"○", "muted"}, name, tag, {"calls off", "muted"}, via, {"jara device allow turns them on", "muted"}}
		case !d.Daemon:
			return []networkCell{{"◐", "accent"}, name, tag, {"daemon stopped", "accent"}, via, {"jara daemon install starts it", "muted"}}
		case len(d.Via) == 0:
			return []networkCell{{"◐", "accent"}, name, tag, {"not linked", "accent"}, via, {"journalctl --user -u jara.service says why", "muted"}}
		}
		return []networkCell{{"●", "green"}, name, tag, {"accepts calls", ""}, via, folders}
	}
	if d.Callable {
		if d.Problem != "" {
			folders = networkCell{shortDeviceError(d.Name, d.Problem), "accent"}
		}
		return []networkCell{{"●", "green"}, name, tag, {"accepts calls", ""}, via, folders}
	}
	seen := "never linked"
	if d.LastSeen > 0 {
		seen = "last linked " + ago(d.LastSeen, now)
	}
	return []networkCell{{"○", "muted"}, name, tag, {"offline", "muted"}, via, {seen, "muted"}}
}

// shortDeviceError drops the "device NAME: " a host puts before a device's
// own error; the row already names it.
func shortDeviceError(name, problem string) string {
	return strings.TrimPrefix(firstLine(problem), "device "+name+": ")
}

// shortHostError keeps the reason of an SSH failure ("Connection timed
// out"), not the address the row already shows.
func shortHostError(problem string) string {
	problem = firstLine(problem)
	if strings.Contains(problem, "ssh: ") {
		if i := strings.LastIndex(problem, ": "); i >= 0 {
			return problem[i+2:]
		}
	}
	return strings.TrimPrefix(problem, "remote network-status: ")
}

// networkOverview is `jara network`.
func networkOverview(c Config) error {
	p := newNetworkPainter()
	if p.color {
		fmt.Fprint(os.Stderr, p.paint("muted", "  Asking your hosts…")+"\r")
	}
	view := collectNetwork(c)
	if p.color {
		fmt.Fprint(os.Stderr, "\x1b[2K")
	}
	fmt.Print(renderNetwork(view, time.Now(), p))
	return nil
}
