import path from "node:path";
import { resolveExports } from "./exports.mjs";
import { bindSource, sourcePosition, localReferences } from "./bindings.mjs";

export function discoverConsumers(
  ts,
  input,
  sourceInputs,
  compiler,
  entries,
  timings,
  catalog,
) {
  const { root, absolute, relative, read } = sourceInputs;
  const { options, sys } = compiler;
  const consumers = [];
  const coverage = {
    complete: true,
    parse_errors: 0,
    issues: [],
    omitted_issues: 0,
    omitted_references: 0,
    scanned_files: 0,
    unscanned_files: 0,
    unresolved_dynamic: 0,
    reexport_limit: false,
    limits: [
      "Import and re-export edges identify consumers, not a complete call graph. Local references resolve import bindings within each consumer; at most 64 are shown per edge. Nonliteral dynamic imports require review.",
      "Package specifiers supplied with --module map to their explicitly selected entries. Other resolution uses the captured repository source inventory, not installed dependency internals.",
    ],
  };
  const issue = (kind, file, position, message, compiler = "") => {
    coverage.complete = false;
    if (coverage.issues.length < 200)
      coverage.issues.push({
        kind,
        compiler,
        path: relative(file),
        ...position,
        message,
      });
    else coverage.omitted_issues++;
  };
  if (input.mode === "callers") {
    let started = performance.now();
    const edges = [];
    const modules = new Map();
    const moduleCache = ts.createModuleResolutionCache(root, (p) => p, options);
    // Caller resolution ranges over the captured source inventory. Package names
    // supplied by --module map to the selected entry; installed dependency trees
    // are irrelevant to this reverse source graph and are not scanned.
    const files = new Set(input.files.map(absolute));
    const dirs = new Set();
    for (const file of files)
      for (
        let dir = path.dirname(file);
        dir.startsWith(root);
        dir = path.dirname(dir)
      ) {
        dirs.add(dir);
        if (dir === root) break;
      }
    const callerHost = {
      ...sys,
      fileExists: (file) => files.has(absolute(file)),
      directoryExists: (dir) => dirs.has(absolute(dir)),
      realpath: (p) => p,
    };
    const resolve = (module, file) =>
      Object.hasOwn(input.modules || {}, module)
        ? absolute(input.modules[module])
        : ts.resolveModuleName(module, file, options, callerHost, moduleCache)
            .resolvedModule?.resolvedFileName;
    let callerBytes = 0;
    for (const file of input.files) {
      if (
        coverage.scanned_files >= 16000 ||
        callerBytes >= 112 * 1024 * 1024
      ) {
        coverage.unscanned_files++;
        issue(
          "unscanned_file",
          file,
          { line: 0, column: 0 },
          "Caller input bound reached or source unavailable",
        );
        continue;
      }
      const text = read(file);
      if (text === undefined) {
        coverage.unscanned_files++;
        issue(
          "unscanned_file",
          file,
          { line: 0, column: 0 },
          "Caller input bound reached or source unavailable",
        );
        continue;
      }
      const size = Buffer.byteLength(text);
      if (size > 112 * 1024 * 1024 - callerBytes) {
        coverage.unscanned_files++;
        issue(
          "unscanned_file",
          file,
          { line: 0, column: 0 },
          "Caller source byte bound reached",
        );
        continue;
      }
      callerBytes += size;
      const callerCompiler = catalog.caller(file);
      if (!callerCompiler.ts) {
        coverage.unscanned_files++;
        issue(
          "compiler_unavailable",
          file,
          { line: 0, column: 0 },
          callerCompiler.scope.error,
        );
        continue;
      }
      const ts = callerCompiler.ts;
      const source = ts.createSourceFile(
        absolute(file),
        text,
        ts.ScriptTarget.Latest,
        true,
      );
      coverage.scanned_files++;
      for (const diagnostic of source.parseDiagnostics) {
        coverage.parse_errors++;
        issue(
          "parse_error",
          file,
          sourcePosition(source, diagnostic.start ?? 0),
          ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"),
          callerCompiler.scope.compiler,
        );
      }
      let checker;
      const summary = {
        imports: new Map(),
        explicit: new Map(),
        stars: [],
        exports: new Map(),
      };
      modules.set(absolute(file), summary);
      const edge = (node, module, names, kind) => {
        const provider = resolve(module, absolute(file));
        for (const name of names) {
          const binding = {
            source: provider,
            external: module,
            imported: name.imported,
          };
          if (kind === "import") summary.imports.set(name.local, binding);
          if (kind === "reexport") {
            if (name.local === "*") summary.stars.push(provider);
            else summary.explicit.set(name.local, binding);
          }
        }
        edges.push({
          compiler: callerCompiler,
          file: absolute(file),
          source: provider,
          module,
          names,
          kind,
          ...sourcePosition(source, node.getStart()),
        });
      };
      const declare = (name) => {
        if (ts.isIdentifier(name))
          summary.explicit.set(name.text, { local: name.text });
        else if (
          ts.isObjectBindingPattern(name) ||
          ts.isArrayBindingPattern(name)
        )
          for (const element of name.elements)
            if (ts.isBindingElement(element)) declare(element.name);
      };
      for (const node of source.statements) {
        if (
          ts.isImportDeclaration(node) &&
          ts.isStringLiteral(node.moduleSpecifier)
        ) {
          const names = [];
          const clause = node.importClause;
          if (clause?.name)
            names.push({ imported: "default", local: clause.name.text });
          const bindings = clause?.namedBindings;
          if (bindings && ts.isNamedImports(bindings))
            for (const e of bindings.elements)
              names.push({
                imported: e.propertyName?.text || e.name.text,
                local: e.name.text,
              });
          else if (bindings)
            names.push({ imported: "*", local: bindings.name.text });
          edge(node, node.moduleSpecifier.text, names, "import");
        } else if (
          ts.isImportEqualsDeclaration(node) &&
          ts.isExternalModuleReference(node.moduleReference) &&
          ts.isStringLiteral(node.moduleReference.expression)
        ) {
          edge(
            node,
            node.moduleReference.expression.text,
            [{ imported: "*", local: node.name.text }],
            "import",
          );
          if (
            node.modifiers?.some(
              (modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword,
            )
          )
            summary.explicit.set(node.name.text, { local: node.name.text });
        } else if (
          ts.isExportDeclaration(node) &&
          node.moduleSpecifier &&
          ts.isStringLiteral(node.moduleSpecifier)
        ) {
          const clause = node.exportClause;
          const names =
            clause && ts.isNamedExports(clause)
              ? clause.elements.map((e) => ({
                  imported: e.propertyName?.text || e.name.text,
                  local: e.name.text,
                }))
              : [{ imported: "*", local: clause?.name?.text || "*" }];
          edge(node, node.moduleSpecifier.text, names, "reexport");
        } else if (
          ts.isExportDeclaration(node) &&
          !node.moduleSpecifier &&
          node.exportClause &&
          ts.isNamedExports(node.exportClause)
        ) {
          for (const e of node.exportClause.elements)
            summary.explicit.set(e.name.text, {
              local: e.propertyName?.text || e.name.text,
            });
        } else if (ts.isExportAssignment(node) && !node.isExportEquals) {
          summary.explicit.set("default", { local: "default" });
        } else if (
          node.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)
        ) {
          if (
            node.modifiers.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword)
          )
            summary.explicit.set("default", {
              local: node.name?.text || "default",
            });
          else if (ts.isVariableStatement(node))
            for (const declaration of node.declarationList.declarations)
              declare(declaration.name);
          else if (node.name && ts.isIdentifier(node.name)) declare(node.name);
        }
      }
      const visit = (node) => {
        if (
          ts.isImportTypeNode(node) &&
          ts.isLiteralTypeNode(node.argument) &&
          ts.isStringLiteral(node.argument.literal)
        ) {
          // import("module").Name and typeof import("module") are source
          // dependencies, but introduce no local binding or runtime import.
          let qualifier = node.qualifier;
          while (qualifier && ts.isQualifiedName(qualifier))
            qualifier = qualifier.left;
          edge(
            node,
            node.argument.literal.text,
            [{ imported: qualifier?.text || "*", local: "" }],
            "type_import",
          );
        } else if (
          ts.isCallExpression(node) &&
          (node.expression.kind === ts.SyntaxKind.ImportKeyword ||
            (ts.isIdentifier(node.expression) &&
              node.expression.text === "require"))
        ) {
          const isRequire = ts.isIdentifier(node.expression);
          if (isRequire) {
            checker ||= bindSource(ts, source);
            // A local function or parameter called require is not the ambient loader.
            if (checker.getSymbolAtLocation(node.expression)) {
              ts.forEachChild(node, visit);
              return;
            }
          }
          const arg = node.arguments[0];
          if (
            arg &&
            (ts.isStringLiteral(arg) || ts.isNoSubstitutionTemplateLiteral(arg))
          )
            edge(node, arg.text, [{ imported: "*", local: "" }], "dynamic");
          else {
            coverage.unresolved_dynamic++;
            issue(
              "nonliteral_import",
              file,
              sourcePosition(source, node.getStart()),
              "Module specifier is not a literal; dependency is unresolved",
            );
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(source);
    }
    timings.caller_parse = performance.now() - started;
    started = performance.now();
    const graphs = input.entries.map((entry) => {
      const limits = { reexport_limit: false };
      const targets = resolveExports(
        modules,
        absolute(entry),
        entries[entry],
        limits,
      );
      if (limits.reexport_limit) {
        coverage.reexport_limit = true;
        issue(
          "reexport_limit",
          entry,
          { line: 0, column: 0 },
          "Export traversal bound reached",
        );
      }
      return { entry, targets };
    });
    timings.caller_graph = performance.now() - started;
    started = performance.now();
    let lastFile, lastReferences;
    for (const edge of edges)
      for (const { entry, targets } of graphs) {
        const names = targets.get(edge.source);
        if (!names) continue;
        const bindings = edge.names.filter(
          (n) => n.imported === "*" || names.has(n.imported),
        );
        // A side-effect import has no binding, but still depends on the module.
        if (!bindings.length && !(edge.kind === "import" && !edge.names.length))
          continue;
        let references = [],
          omitted = 0,
          complete = true;
        if (edge.kind === "import" && bindings.length) {
          if (lastFile !== edge.file) {
            lastReferences = localReferences(
              edge.compiler.ts,
              edge.compiler.ts.createSourceFile(
                edge.file,
                read(edge.file),
                edge.compiler.ts.ScriptTarget.Latest,
                true,
              ),
            );
            lastFile = edge.file;
          }
          const locals = new Set(bindings.map((binding) => binding.local));
          references = lastReferences.references
            .filter((reference) => locals.has(reference.name))
            .slice(0, 64);
          omitted =
            [...locals].reduce(
              (total, name) => total + (lastReferences.counts.get(name) || 0),
              0,
            ) - references.length;
          complete = lastReferences.complete && omitted === 0;
          coverage.omitted_references += omitted;
          if (!complete) coverage.complete = false;
        }
        consumers.push({
          entry,
          column: edge.column,
          references_complete: complete,
          omitted_references: omitted,
          compiler: edge.compiler.scope.compiler,
          path: relative(edge.file),
          line: edge.line,
          module: edge.module,
          source: relative(edge.source),
          kind: edge.kind,
          bindings,
          references,
        });
      }
    consumers.sort(
      (a, b) =>
        a.entry.localeCompare(b.entry) ||
        a.path.localeCompare(b.path) ||
        a.line - b.line ||
        a.column - b.column,
    );
    timings.caller_references = performance.now() - started;
  }

  return { consumers, coverage };
}
