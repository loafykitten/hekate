import { createRequire } from "node:module";
import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";

// typescript-eslint still imports the JS compiler API from "typescript" and only supports
// versions below 6.1; the project's typescript is the native 7.x compiler, which has no such
// API. Point typescript-eslint's `require("typescript")` at the TypeScript 6 compatibility
// package (a devDependency). Linting is the only place that uses it.
const require = createRequire(import.meta.url);
const Module = require("node:module");
const ts6 = require.resolve("@typescript/typescript6");
const resolveFilename = Module._resolveFilename;
Module._resolveFilename = function (request, ...rest) {
  return resolveFilename.call(this, request === "typescript" ? ts6 : request, ...rest);
};
const tseslint = (await import("typescript-eslint")).default;

export default tseslint.config(
  { ignores: ["dist", "dev-dist", "verify-out", "node_modules"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: { globals: globals.browser },
    plugins: { "react-hooks": reactHooks, "react-refresh": reactRefresh },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
    },
  },
  {
    files: ["*.ts", "*.js", "server/**/*.ts", "scripts/**/*.mjs"],
    // The verify scripts also run code inside the page via page.evaluate.
    languageOptions: { globals: { ...globals.node, ...globals.browser } },
  },
);
