// Chart colours read the brand tokens (src/styles/tokens.css), so every
// chart follows the theme instead of carrying its own hex values.
export const chart = {
  grid: "var(--tv-line)",
  axis: "var(--tv-ink-muted)",
  primary: "var(--tv-accent)",
  positive: "var(--tv-positive)",
  rest: "var(--tv-line-strong)",
  tooltip: {
    backgroundColor: "var(--tv-raised)",
    border: "1px solid var(--tv-line-strong)",
    color: "var(--tv-ink)",
    fontSize: 12,
  },
} as const;
