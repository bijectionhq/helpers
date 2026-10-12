import path from "node:path";

// Bind one consumer at a time, without resolving imports, loading libraries or
// retaining a repository-wide program. Alias identity distinguishes actual local
// uses from shadowing declarations and equally spelled property names.
export function bindSource(ts, source) {
  const program = ts.createProgram(
    [source.fileName],
    {
      allowJs: true,
      noLib: true,
      noResolve: true,
      types: [],
      target: ts.ScriptTarget.Latest,
      jsx: ts.JsxEmit.Preserve,
    },
    {
      getSourceFile: (file) => (file === source.fileName ? source : undefined),
      getDefaultLibFileName: () => "",
      writeFile: () => {},
      getCurrentDirectory: () => path.dirname(source.fileName),
      getCanonicalFileName: (file) => file,
      useCaseSensitiveFileNames: () => true,
      getNewLine: () => "\n",
      fileExists: (file) => file === source.fileName,
      readFile: () => undefined,
      directoryExists: () => false,
    },
  );
  return program.getTypeChecker();
}

export function sourcePosition(source, offset) {
  const { line } = source.getLineAndCharacterOfPosition(offset);
  const start = source.getPositionOfLineAndCharacter(line, 0);
  return {
    line: line + 1,
    column: Buffer.byteLength(source.text.slice(start, offset), "utf8") + 1,
  };
}

export function localReferences(ts, source) {
  if (source.parseDiagnostics.length)
    return { references: [], counts: new Map(), complete: false };
  const checker = bindSource(ts, source);
  const imports = new Map();
  for (const symbol of checker.getSymbolsInScope(source, ts.SymbolFlags.Alias))
    if (
      symbol.declarations?.some(
        (node) =>
          ts.isImportSpecifier(node) ||
          ts.isImportClause(node) ||
          ts.isNamespaceImport(node) ||
          ts.isImportEqualsDeclaration(node),
      )
    )
      imports.set(symbol, { name: symbol.name, count: 0 });
  const names = new Set([...imports.values()].map((binding) => binding.name));
  const references = [];
  const visit = (node) => {
    if (
      ts.isImportDeclaration(node) ||
      ts.isImportEqualsDeclaration(node) ||
      ts.isExportDeclaration(node)
    )
      return;
    if (ts.isIdentifier(node) && names.has(node.text)) {
      const symbol =
        ts.isShorthandPropertyAssignment(node.parent) &&
        node.parent.name === node
          ? checker.getShorthandAssignmentValueSymbol(node.parent)
          : checker.getSymbolAtLocation(node);
      const binding = imports.get(symbol);
      if (binding) {
        binding.count++;
        if (binding.count <= 64)
          references.push({
            ...sourcePosition(source, node.getStart()),
            name: binding.name,
          });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return {
    references,
    counts: new Map(
      [...imports.values()].map((binding) => [binding.name, binding.count]),
    ),
    complete: true,
  };
}
