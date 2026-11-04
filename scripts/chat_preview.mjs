// Render an actual Jara ANSI frame fixture as a dependency-free SVG preview.
// Usage: node scripts/chat_preview.mjs FRAME.ansi OUTPUT.svg [COLUMNS]
import { readFileSync, writeFileSync } from "node:fs";

const [input, output, columns] = process.argv.slice(2);
if (!input || !output) {
  throw new Error("usage: node scripts/chat_preview.mjs FRAME.ansi OUTPUT.svg [COLUMNS]");
}
const lines = readFileSync(input, "utf8").replace(/\r/g, "").replace(/\n$/, "").split("\n");
const segmenter = new Intl.Segmenter("en", { granularity: "grapheme" });
const escape = (text) => text.replace(/&/g, "&amp;").replace(/</g, "&lt;")
  .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
const cells = (text) => [...segmenter.segment(text)].reduce((total, { segment }) => {
  const cp = segment.codePointAt(0);
  const wide = /\p{Extended_Pictographic}/u.test(segment) ||
    (cp >= 0x1100 && cp <= 0x115f) || (cp >= 0x2e80 && cp <= 0xa4cf) ||
    (cp >= 0xac00 && cp <= 0xd7a3) || (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe10 && cp <= 0xfe6f) || (cp >= 0xff01 && cp <= 0xff60) ||
    (cp >= 0x20000 && cp <= 0x3fffd);
  return total + (wide ? 2 : /^\p{Mark}+$/u.test(segment) ? 0 : 1);
}, 0);
const strip = (text) => text.replace(/\x1b\[[0-9;]*m/g, "");
const cols = Number(columns) || Math.max(...lines.map((line) => cells(strip(line))));
const cw = 8.4, lh = 21, padding = 18;
const width = cols * cw + padding * 2, height = lines.length * lh + padding * 2;
const parts = [
  `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" role="img" aria-label="Jara conversation terminal preview">`,
  `<rect width="100%" height="100%" rx="10" fill="#0f131e"/>`,
  `<g font-family="ui-monospace, SFMono-Regular, Menlo, Consolas, monospace" font-size="14" xml:space="preserve">`,
];
for (const [row, line] of lines.entries()) {
  let x = padding, fg = "#e5ebf8", bg = "#0f131e", bold = false, dim = false;
  const draw = (text) => {
    if (!text) return;
    const runWidth = cells(text) * cw;
    const y = padding + row * lh;
    parts.push(`<rect x="${x}" y="${y}" width="${runWidth}" height="${lh}" fill="${bg}"/>`);
    if (text.trim()) {
      parts.push(`<text x="${x}" y="${y + 15}" fill="${fg}"${bold ? ' font-weight="700"' : ""}${dim ? ' opacity="0.7"' : ""}>${escape(text)}</text>`);
    }
    x += runWidth;
  };
  let offset = 0;
  for (const match of line.matchAll(/\x1b\[([0-9;]*)m/g)) {
    draw(line.slice(offset, match.index));
    const codes = match[1] === "" ? [0] : match[1].split(";").map(Number);
    for (let i = 0; i < codes.length; i++) {
      const code = codes[i];
      if (code === 0) { fg = "#e5ebf8"; bg = "#0f131e"; bold = false; dim = false; }
      if (code === 1) bold = true;
      if (code === 2) dim = true;
      if (code === 22) { bold = false; dim = false; }
      if ((code === 38 || code === 48) && codes[i + 1] === 2) {
        const color = `rgb(${codes[i + 2]},${codes[i + 3]},${codes[i + 4]})`;
        if (code === 38) fg = color; else bg = color;
        i += 4;
      }
    }
    offset = match.index + match[0].length;
  }
  draw(line.slice(offset));
}
parts.push("</g>", "</svg>");
writeFileSync(output, parts.join("\n") + "\n");
