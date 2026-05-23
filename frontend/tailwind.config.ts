import type { Config } from "tailwindcss";

// Talyvor design tokens — dark-mode-first. The hex values here are the
// single source of truth; every component references these via the
// Tailwind class names (bg-surface, text-muted, etc.).
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        bg: "#0c0e12",
        surface: "#13161c",
        border: "#1e2330",
        text: "#d4d8e2",
        muted: "#8892a4",
        accent: "#f0a030",
        "status-backlog": "#64748b",
        "status-todo": "#94a3b8",
        "status-progress": "#3b82f6",
        "status-review": "#f59e0b",
        "status-done": "#22c55e",
        "status-cancelled": "#ef4444",
        "priority-urgent": "#ef4444",
        "priority-high": "#f97316",
        "priority-medium": "#eab308",
        "priority-low": "#94a3b8",
      },
      fontFamily: {
        mono: [
          "IBM Plex Mono",
          "ui-monospace",
          "SFMono-Regular",
          "monospace",
        ],
        sans: ["Inter", "ui-sans-serif", "system-ui", "sans-serif"],
      },
    },
  },
  plugins: [],
} satisfies Config;
