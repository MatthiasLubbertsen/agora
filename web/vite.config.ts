import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// The build lands inside the Go package so `go:embed` ships it in the binary.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/auth": "http://localhost:8080",
      "/oauth": "http://localhost:8080",
    },
  },
});
