export function captureExports(ts, program, input, source) {
  const { absolute, relative } = source;
  const checker = program.getTypeChecker();
  const flags =
    ts.TypeFormatFlags.NoTruncation |
    ts.TypeFormatFlags.InTypeAlias |
    ts.TypeFormatFlags.UseFullyQualifiedType;
  // Only literal-union ordering is normalized. Overloads, intersections and
  // structural unions retain their order because type inference can observe it.
  const canonicalText = (text, signature = false) => {
    const prefix = signature ? "interface __Concave { " : "type __Concave = ";
    const parsed = ts.createSourceFile(
      "__concave.ts",
      prefix + text + (signature ? "; }" : ";"),
      ts.ScriptTarget.Latest,
      true,
    );
    if (parsed.parseDiagnostics.length) return text;
    const edits = [];
    const visit = (node) => {
      if (ts.isUnionTypeNode(node) && node.types.every(ts.isLiteralTypeNode)) {
        edits.push({
          start: node.getStart(parsed) - prefix.length,
          end: node.end - prefix.length,
          text: node.types
            .map((member) => member.getText(parsed))
            .sort()
            .join(" | "),
        });
      } else ts.forEachChild(node, visit);
    };
    visit(parsed);
    for (const edit of edits.sort((a, b) => b.start - a.start))
      text = text.slice(0, edit.start) + edit.text + text.slice(edit.end);
    return text;
  };
  const memberName = (symbol) => {
    if (!symbol.name.startsWith("__@")) return symbol.name;
    const declaration = symbol.declarations?.find(
      (d) => d.name && ts.isComputedPropertyName(d.name),
    );
    if (!declaration)
      throw new Error(
        `Computed member has no stable declaration: ${symbol.name}`,
      );
    const expression = declaration.name.expression;
    if (program.isSourceFileDefaultLibrary(declaration.getSourceFile()))
      return declaration.name.getText();
    let target = checker.getSymbolAtLocation(
      ts.isPropertyAccessExpression(expression) ? expression.name : expression,
    );
    if (target?.flags & ts.SymbolFlags.Alias)
      target = checker.getAliasedSymbol(target);
    if (!target?.declarations?.length)
      throw new Error(
        `Computed member has no stable symbol: ${expression.getText()}`,
      );
    const identities = target.declarations
      .map((d) => {
        const names = [];
        for (
          let parent = d;
          parent && !ts.isSourceFile(parent);
          parent = parent.parent
        ) {
          if (parent.name && ts.isIdentifier(parent.name))
            names.unshift(parent.name.text);
          else if (ts.isFunctionLike(parent) || ts.isClassLike(parent))
            names.unshift(`anonymous@${parent.pos}`);
        }
        return relative(d.getSourceFile().fileName) + "#" + names.join("/");
      })
      .sort();
    return "[unique " + identities.join(", ") + "]";
  };
  const describe = (type, node) => ({
    text: canonicalText(checker.typeToString(type, node, flags)),
    calls: type
      .getCallSignatures()
      .map((s) =>
        canonicalText(checker.signatureToString(s, node, flags), true),
      ),
    constructors: type
      .getConstructSignatures()
      .map((s) =>
        canonicalText(checker.signatureToString(s, node, flags), true),
      ),
    members: type
      .getProperties()
      .map((p) => ({
        name: memberName(p),
        optional: !!(p.flags & ts.SymbolFlags.Optional),
        modifiers: ts.getCombinedModifierFlags(
          p.valueDeclaration || p.declarations?.[0] || node,
        ),
        constant:
          p.declarations?.[0] && ts.isEnumMember(p.declarations[0])
            ? checker.getConstantValue(p.declarations[0])
            : undefined,
        type: canonicalText(
          checker.typeToString(
            checker.getTypeOfSymbolAtLocation(
              p,
              p.valueDeclaration || p.declarations?.[0] || node,
            ),
            node,
            flags,
          ),
        ),
      }))
      .sort((a, b) => a.name.localeCompare(b.name)),
  });
  const entries = {};
  for (const file of input.entries) {
    const source = program.getSourceFile(absolute(file));
    const symbol = source && checker.getSymbolAtLocation(source);
    if (!symbol) throw new Error(`Not a TypeScript module: ${file}`);
    entries[file] = {
      exports: checker
        .getExportsOfModule(symbol)
        .map((e) => {
          const target =
            e.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(e) : e;
          const node =
            target.valueDeclaration || target.declarations?.[0] || source;
          const value =
            target.flags & ts.SymbolFlags.Value
              ? describe(checker.getTypeOfSymbolAtLocation(target, node), node)
              : null;
          const type =
            target.flags & ts.SymbolFlags.Type
              ? describe(checker.getDeclaredTypeOfSymbol(target), node)
              : null;
          const result = {
            name: e.name,
            owner: target.name,
            declarations: [
              ...new Set(
                (target.declarations || []).map((d) =>
                  relative(d.getSourceFile().fileName),
                ),
              ),
            ].sort(),
            value,
            type,
          };
          if (JSON.stringify(result).length > 256 * 1024)
            throw new Error(`Export signature bound exceeded: ${e.name}`);
          return result;
        })
        .sort((a, b) => a.name.localeCompare(b.name)),
      runtime: [],
    };
  }

  return entries;
}
