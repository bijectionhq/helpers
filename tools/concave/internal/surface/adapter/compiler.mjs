import path from "node:path";
import { createRequire } from "node:module";
import { digest } from "./inputs.mjs";

// The selected project owns API types and module-resolution options. Caller syntax
// belongs to the nearest package declaring TypeScript, or explicitly to the selected
// project when no package declares it. Every lookup and executable is an input.
export function compilerCatalog(source, anchor) {
  const loaded = new Map(),
    engines = new Map(),
    owners = new Map(),
    contexts = new Map();
  function load(owner, basis) {
    const key = owner + "\0" + basis;
    if (contexts.has(key)) return contexts.get(key);
    const req = createRequire(source.absolute(owner));
    let packageDirectory, manifest;
    for (const directory of req.resolve.paths("typescript") || []) {
      // Global NODE_PATH directories are not repository tool dependencies.
      if (path.basename(directory) !== "node_modules") continue;
      const text = source.read(path.join(directory, "typescript/package.json"));
      if (text !== undefined) {
        packageDirectory = path.join(directory, "typescript");
        manifest = JSON.parse(text);
        break;
      }
    }
    if (!packageDirectory) {
      const context = {
        scope: {
          owner,
          basis,
          compiler: "",
          typescript: "",
          files: 0,
          error: `Installed TypeScript is unavailable for ${owner}; install that package's pinned dependencies.`,
        },
      };
      contexts.set(key, context);
      return context;
    }
    // Capture each physical executable once. Go also validates the logical-to-real
    // mapping so retargeting a dependency link cannot reuse stale evidence.
    const logical = path.join(packageDirectory, manifest.main || "index.js");
    const file = req.resolve(packageDirectory);
    if (!loaded.has(file)) {
      const identity = digest(source.read(file));
      // Caller hosts perform no filesystem access. Identical compiler code can
      // share syntax/binding machinery; ownership and tool inputs stay separate.
      // The selected API compiler loads first and keeps its own library origin.
      if (!engines.has(identity)) {
        if (engines.size >= 16)
          throw new Error(
            "Compiler ownership exceeds 16 distinct TypeScript engines",
          );
        engines.set(identity, req(file));
      }
      loaded.set(file, engines.get(identity));
    }
    const ts = loaded.get(file);
    const context = {
      ts,
      req,
      scope: {
        owner,
        basis,
        compiler: source.relative(logical),
        resolved_compiler: source.relative(file),
        typescript: ts.version,
        files: 0,
      },
    };
    contexts.set(key, context);
    return context;
  }
  function packageOwner(directory) {
    if (owners.has(directory)) return owners.get(directory);
    const manifest = path.join(directory, "package.json");
    const text = source.read(manifest);
    let owner;
    if (text !== undefined) {
      const pkg = JSON.parse(text);
      if (
        pkg.dependencies?.typescript ||
        pkg.devDependencies?.typescript ||
        pkg.peerDependencies?.typescript
      )
        owner = source.relative(manifest);
    }
    if (!owner && directory !== source.root)
      owner = packageOwner(path.dirname(directory));
    owners.set(directory, owner);
    return owner;
  }
  const selected = load(anchor, "selected_project");
  if (!selected.ts) throw new Error(selected.scope.error);
  return {
    selected,
    caller(file) {
      const owner = packageOwner(path.dirname(source.absolute(file)));
      const context = owner ? load(owner, "package") : selected;
      context.scope.files++;
      return context;
    },
    scopes: () =>
      [...contexts.values()]
        .map((context) => context.scope)
        .sort(
          (a, b) =>
            a.owner.localeCompare(b.owner) || a.basis.localeCompare(b.basis),
        ),
  };
}

export function compile(ts, input, source) {
  const { read, exists, directoryExists, absolute } = source;
  const sys = {
    ...ts.sys,
    readFile: read,
    fileExists: exists,
    directoryExists,
  };
  let options = {
    noEmit: true,
    skipLibCheck: true,
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    jsx: ts.JsxEmit.React,
  };
  let configErrors = [];
  if (input.project) {
    const config = ts.readConfigFile(absolute(input.project), read);
    if (config.error) configErrors.push(config.error);
    else {
      const parsed = ts.parseJsonConfigFileContent(
        config.config,
        sys,
        path.dirname(absolute(input.project)),
      );
      options = {
        ...parsed.options,
        noEmit: true,
        emitDeclarationOnly: false,
        declaration: false,
        declarationMap: false,
      };
      configErrors.push(...parsed.errors);
    }
  }
  const host = ts.createCompilerHost(options);
  host.readFile = read;
  host.fileExists = exists;
  host.directoryExists = directoryExists;
  const program = ts.createProgram(input.entries.map(absolute), options, host);

  return { program, options, configErrors, sys };
}

export function captureDiagnostics(ts, program, configErrors, source) {
  const { relative } = source;
  const diagnostics = [...configErrors, ...ts.getPreEmitDiagnostics(program)]
    .slice(0, 200)
    .map((d) => ({
      code: d.code,
      path: d.file ? relative(d.file.fileName) : "",
      line:
        d.file && d.start !== undefined
          ? d.file.getLineAndCharacterOfPosition(d.start).line + 1
          : 0,
      message: ts.flattenDiagnosticMessageText(d.messageText, "\n"),
    }));

  return diagnostics;
}
