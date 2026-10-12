import fs from "node:fs";
import { StaleInput, digest, captureInputs } from "./inputs.mjs";
import { compilerCatalog, compile, captureDiagnostics } from "./compiler.mjs";
import { captureExports } from "./signatures.mjs";
import { captureRuntime } from "./runtime.mjs";
import { discoverConsumers } from "./callers.mjs";

async function analyze(input, timings) {
  let started = performance.now();
  const source = captureInputs(process.cwd());
  const catalog = compilerCatalog(source, input.project || input.entries[0]);
  const { ts, req } = catalog.selected;
  const nvm = source.read(".nvmrc");
  if (nvm && nvm.trim().replace(/^v/, "") !== process.versions.node)
    throw new Error(
      `Node ${process.versions.node} differs from .nvmrc ${nvm.trim()}; use the repository toolchain`,
    );
  timings.setup = performance.now() - started;
  started = performance.now();
  const compiler = compile(ts, input, source);
  timings.compile = performance.now() - started;
  started = performance.now();
  const entries = captureExports(ts, compiler.program, input, source);
  timings.exports = performance.now() - started;
  started = performance.now();
  const esbuild =
    input.mode === "snapshot"
      ? await captureRuntime(req, input, source, entries)
      : "";
  timings.runtime = performance.now() - started;
  started = performance.now();
  const diagnostics = captureDiagnostics(
    ts,
    compiler.program,
    compiler.configErrors,
    source,
  );
  timings.diagnostics = performance.now() - started;
  const { consumers, coverage } = discoverConsumers(
    ts,
    input,
    source,
    compiler,
    entries,
    timings,
    catalog,
  );
  return {
    compilers: catalog.scopes(),
    entries,
    consumers,
    coverage,
    diagnostics,
    toolchain: { node: process.versions.node, typescript: ts.version, esbuild },
    options_sha256: digest(JSON.stringify(compiler.options)),
    inputs: source.manifest(),
    limits: [
      "Working source snapshot; no historical compiler overlay. Compiler diagnostics prevent a complete surface claim. Runtime exports are bundled names with packages external; modules are never executed.",
      "Structural signatures and export ownership are review evidence, not behavioral or binary compatibility proof.",
    ],
  };
}

const request = JSON.parse(fs.readFileSync(0, "utf8"));
if (request.protocol !== 1)
  throw new Error("Unsupported Concave compiler protocol");
const timings = {};
try {
  process.stdout.write(
    JSON.stringify({
      protocol: 1,
      analysis: await analyze(request.options, timings),
      timings,
    }),
  );
} catch (error) {
  process.stdout.write(
    JSON.stringify({
      protocol: 1,
      error: {
        kind: error instanceof StaleInput ? "stale_inputs" : "analysis_failed",
        message: error.message,
      },
    }),
  );
}
