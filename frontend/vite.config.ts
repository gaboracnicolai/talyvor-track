import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Vite config — keep build defaults plus a `~` alias rooted at src/.
// Dev server proxies /v1 to the Go backend so the frontend can run
// against `localhost:5173` without CORS configuration on the API side.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "~": path.resolve(__dirname, "src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/v1": {
        target: "http://localhost:3000",
        changeOrigin: true,
        ws: true, // proxy /v1/ws too
      },
    },
  },
});
