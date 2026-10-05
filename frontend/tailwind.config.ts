import type { Config } from "tailwindcss";

// Talyvor brand v4 tokens. The values live in src/styles/tokens.css (a
// verbatim copy of the brand folder's tokens.css); every class here reads
// a --tv-* variable. color-mix keeps Tailwind's opacity modifiers
// (bg-accent/10, border-border/80) working on a variable colour.
const tv = (name: string) =>
  `color-mix(in srgb, var(--tv-${name}) calc(<alpha-value> * 100%), transparent)`;

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        bg: tv("canvas"),
        surface: tv("surface"),
        raised: tv("raised"),
        border: tv("line"),
        "border-strong": tv("line-strong"),
        text: tv("ink"),
        muted: tv("ink-muted"),
        label: tv("label"),
        accent: tv("accent"),
        "accent-hover": tv("accent-hover"),
        "on-accent": tv("on-accent"),
        "accent-tint": tv("accent-tint"),
        "status-backlog": tv("ink-muted"),
        "status-todo": tv("label"),
        "status-progress": tv("accent"),
        "status-review": tv("caution"),
        "status-done": tv("positive"),
        "status-cancelled": tv("critical"),
        "priority-urgent": tv("critical"),
        "priority-high": tv("caution"),
        "priority-medium": tv("label"),
        "priority-low": tv("ink-muted"),
      },
      fontFamily: {
        mono: ["IBM Plex Mono", "ui-monospace", "SFMono-Regular", "monospace"],
        sans: ["Space Grotesk", "system-ui", "sans-serif"],
      },
    },
  },
  plugins: [],
} satisfies Config;
