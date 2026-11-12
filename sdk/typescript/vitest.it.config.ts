import { defineConfig } from "vitest/config";

// Integration tests against a live gateway (GATEWAY_URL, GATEWAY_API_KEY).
export default defineConfig({
  test: {
    include: ["test/**/*.it.test.ts"],
    environment: "node",
    testTimeout: 60_000,
  },
});
