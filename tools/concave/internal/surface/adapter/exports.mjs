// Keep compact binding origins, not ASTs. Two different origins are enough to
// mark an ambiguous star export; another route to the same origin is a diamond.
// Explicit exports take precedence over stars regardless of statement order.
export function resolveExports(modules, entryPath, entry, coverage) {
  for (const summary of modules.values()) summary.exports = new Map();
  const selected = new Set(entry.exports.map((item) => item.name));
  let slots = 0;
  const merge = (file, name, origins) => {
    const summary = modules.get(file);
    let destination = summary.exports.get(name);
    if (!destination) {
      if (slots >= 200000) {
        coverage.reexport_limit = true;
        return false;
      }
      destination = new Map();
      summary.exports.set(name, destination);
      slots++;
    }
    let changed = false;
    for (const [origin, target] of origins) {
      const reachesEntry = target || (file === entryPath && selected.has(name));
      if (!destination.has(origin) && destination.size < 2) {
        destination.set(origin, reachesEntry);
        changed = true;
      } else if (
        destination.has(origin) &&
        reachesEntry &&
        !destination.get(origin)
      ) {
        destination.set(origin, true);
        changed = true;
      }
    }
    return changed;
  };
  const links = [];
  for (const [file, summary] of modules) {
    for (const [name, exported] of summary.explicit) {
      const binding = exported.local
        ? summary.imports.get(exported.local)
        : exported;
      if (binding) links.push({ file, name, ...binding });
      else merge(file, name, [[`${file}\0${exported.local}`, false]]);
    }
  }
  // Resolve ambiguity before propagating namespace reachability. A namespace
  // must not retain a target seen before a second star made that name ambiguous.
  for (const namespaces of [false, true]) {
    let changed = true;
    for (
      let round = 0;
      changed && round < 64 && !coverage.reexport_limit;
      round++
    ) {
      changed = false;
      for (const link of links) {
        const provider = modules.get(link.source);
        if (!provider) {
          changed =
            merge(link.file, link.name, [
              [`${link.source || link.external}\0${link.imported}`, false],
            ]) || changed;
          continue;
        }
        if (link.imported === "*") {
          const target =
            namespaces &&
            (link.source === entryPath ||
              [...provider.exports.values()].some(
                (origins) => origins.size === 1 && [...origins.values()][0],
              ));
          changed =
            merge(link.file, link.name, [[`${link.source}\0*`, target]]) ||
            changed;
        } else {
          const origins = provider.exports.get(link.imported);
          if (origins)
            changed = merge(link.file, link.name, origins) || changed;
        }
      }
      for (const [file, summary] of modules)
        for (const provider of summary.stars)
          for (const [name, origins] of modules.get(provider)?.exports || [])
            if (name !== "default" && !summary.explicit.has(name))
              changed = merge(file, name, origins) || changed;
    }
    coverage.reexport_limit ||= changed;
  }
  const targets = new Map([[entryPath, selected]]);
  for (const [file, summary] of modules) {
    if (file === entryPath) continue;
    const names = new Set();
    for (const [name, origins] of summary.exports)
      if (origins.size === 1 && [...origins.values()][0]) names.add(name);
    if (names.size) targets.set(file, names);
  }
  return targets;
}
