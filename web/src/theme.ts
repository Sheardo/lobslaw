/** Per-bot identity.
 *
 * The single most useful thing this console does visually. A team
 * rendered as rows of text is a list you have to read; the same team
 * with a colour each is one you recognise — the same orange in the
 * sidebar, on the avatar, and down the edge of its queue rows.
 *
 * Derived from the id rather than stored, so a bot created thirty
 * seconds ago already has an identity and there is no colour field to
 * pick, migrate, or leave unset.
 *
 * Golden-angle stepping over the hash spreads adjacent ids far apart,
 * so "eng" and "engineering" do not land on nearly the same colour —
 * which is exactly when telling them apart matters.
 */
export function botHue(id: string): number {
  let hash = 0;
  for (let i = 0; i < id.length; i++) hash = (hash * 31 + id.charCodeAt(i)) >>> 0;
  return Math.round(((hash % 997) * 137.508) % 360);
}

/** CSS custom properties a bot's elements read from.
 *
 * Saturation and lightness are fixed so every bot reads at the same
 * weight against the dark canvas — a hue landing on yellow must not
 * shout louder than one landing on blue.
 */
export function botVars(id: string): React.CSSProperties {
  const h = botHue(id);
  return {
    "--bot": `hsl(${h} 55% 45%)`,
    "--bot-fg": `hsl(${h} 75% 74%)`,
    "--bot-bg": `hsl(${h} 42% 13%)`,
    "--bot-br": `hsl(${h} 40% 30%)`,
    "--botfg": `hsl(${h} 75% 74%)`,
  } as React.CSSProperties;
}

/** Two characters at most: three is a word, and a word needs reading
 * rather than recognising. */
export function initials(name: string): string {
  const words = name.trim().split(/[\s\-_]+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}
