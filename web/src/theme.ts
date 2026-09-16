import { createSystem, defaultConfig, defineConfig } from "@chakra-ui/react";

/** The console's look.
 *
 * Dark and not switchable. This sits beside a terminal, and a light
 * admin panel in that company reads as a different application.
 *
 * Token paths are FLAT — bg.canvas, fg.hi, edge.mid — with no DEFAULT
 * nesting and no semantic-token indirection. The first version used
 * both, neither resolved, and the result was near-black text on a
 * near-black card plus a white slab through the chat where the body
 * background never applied. A palette that fails silently is worse
 * than an ugly one: nothing errors, it just looks broken.
 */
const config = defineConfig({
  globalCss: {
    "html, body, #root": {
      height: "100%",
      background: "#0B0B0D",
      color: "#EDEDF0",
      fontVariantNumeric: "tabular-nums",
    },
    "*::selection": { background: "#3A1E16", color: "#EDEDF0" },
    "::-webkit-scrollbar": { width: "10px", height: "10px" },
    "::-webkit-scrollbar-thumb": {
      background: "#232329",
      borderRadius: "9999px",
      border: "2px solid #0B0B0D",
    },
    "::-webkit-scrollbar-track": { background: "transparent" },
    a: { color: "inherit", textDecoration: "none" },
  },
  theme: {
    tokens: {
      fonts: {
        heading: { value: 'ui-sans-serif, -apple-system, "Segoe UI", Inter, system-ui, sans-serif' },
        body: { value: 'ui-sans-serif, -apple-system, "Segoe UI", Inter, system-ui, sans-serif' },
        mono: { value: 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace' },
      },
      colors: {
        bg: {
          canvas: { value: "#0B0B0D" },
          s1: { value: "#121215" },
          s2: { value: "#191920" },
          s3: { value: "#24242C" },
        },
        // Three text weights, and the contrast between them is the
        // whole hierarchy. hi is near-white on purpose: the subject of
        // a queue item is what somebody scans for, and the first
        // version had it dimmer than the body text underneath it.
        fg: {
          hi: { value: "#F2F2F5" },
          mid: { value: "#A8A8B4" },
          low: { value: "#6B6B78" },
        },
        edge: {
          soft: { value: "#1E1E25" },
          mid: { value: "#2C2C36" },
          hard: { value: "#3E3E4A" },
        },
        brand: {
          solid: { value: "#F0713F" },
          hover: { value: "#FF8355" },
          dim: { value: "#3A1D12" },
        },
        st: {
          pending: { value: "#8A8A96" },
          claimed: { value: "#5B9DF0" },
          done: { value: "#4FB477" },
          failed: { value: "#F0595F" },
          cancelled: { value: "#D39544" },
        },
      },
      radii: {
        card: { value: "10px" },
        control: { value: "8px" },
      },
    },
  },
});

export const system = createSystem(defaultConfig, config);

/** botHue derives a stable colour from a bot's id.
 *
 * The single most useful thing this console does visually. A team
 * rendered as rows of text is a list you have to read; the same team
 * with a colour each is one you recognise — the same orange in the
 * sidebar, on the avatar, and down the edge of its queue rows.
 *
 * Derived rather than stored so a bot created thirty seconds ago
 * already has an identity, with no colour field to pick or migrate.
 * Golden-angle stepping spreads adjacent ids far apart, so "eng" and
 * "engineering" do not land on nearly the same colour — which is
 * exactly when telling them apart matters.
 */
export function botHue(id: string): number {
  let hash = 0;
  for (let i = 0; i < id.length; i++) hash = (hash * 31 + id.charCodeAt(i)) >>> 0;
  return Math.round(((hash % 997) * 137.508) % 360);
}

/** botColors returns the set a bot is drawn with.
 *
 * Saturation and lightness are fixed so every bot reads at the same
 * weight against the dark canvas — a hue that landed on yellow must
 * not shout louder than one that landed on blue.
 */
export function botColors(id: string) {
  const h = botHue(id);
  return {
    solid: `hsl(${h} 64% 60%)`,
    text: `hsl(${h} 72% 74%)`,
    muted: `hsl(${h} 38% 15%)`,
    border: `hsl(${h} 40% 34%)`,
  };
}

/** Two characters at most: three is a word, and a word needs reading
 * rather than recognising. */
export function initials(name: string): string {
  const words = name.trim().split(/[\s\-_]+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}
