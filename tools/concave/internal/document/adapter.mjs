// Read-only MDX facts. Concave owns correspondence, approval and source edits.
import { createRequire } from "node:module";
import { createHash } from "node:crypto";
import path from "node:path";
import fs from "node:fs";
import { pathToFileURL } from "node:url";
const input = [];
let size = 0;
for await (const chunk of process.stdin) {
  size += chunk.length;
  if (size > 128 * 1024 * 1024) throw new Error("MDX input exceeds bound");
  input.push(chunk);
}
const request = JSON.parse(Buffer.concat(input));
if (request.format !== 1 || request.files.length > 4096)
  throw new Error("Invalid MDX batch");
if (
  process.version.slice(1) !==
  fs.readFileSync(path.join(request.root, ".nvmrc"), "utf8").trim()
)
  throw new Error("Use the repository pinned Node for MDX analysis");
const require = createRequire(
  path.join(request.root, "npm-packages/docs/package.json"),
);
const core = createRequire(require.resolve("@docusaurus/core/package.json"));
const loader = createRequire(core.resolve("@docusaurus/mdx-loader"));
const matter = loader("gray-matter");
// gray-matter's own YAML engine decides what a frontmatter comment is.
const yaml = createRequire(loader.resolve("gray-matter"))("js-yaml");
const { createProcessor } = await import(loader.resolve("@mdx-js/mdx"));
const { default: gfm } = await import(loader.resolve("remark-gfm"));
const processors = Object.fromEntries(
  ["md", "mdx"].map((format) => [
    format,
    createProcessor({ format, remarkPlugins: [gfm] }),
  ]),
);
if (request.recovery) {
  const { recoverSnippets, snippetRenderer } = await import(
    pathToFileURL(
      path.join(request.root, "npm-packages/docs/recovery/snippets.mjs"),
    )
  );
  const read = (name) => {
    if (!Object.hasOwn(request.recovery.inputs, name))
      throw new Error(`Undeclared recovery input: ${name}`);
    return Buffer.from(request.recovery.inputs[name]);
  };
  const result = recoverSnippets({
    inherited: request.recovery.upstream,
    current: request.files[0].source,
    native: request.files[0].source,
    sourcePath: request.recovery.source_path,
    read,
    write() {
      throw new Error("Recovery cannot write generated sources");
    },
    render: snippetRenderer(
      read("npm-packages/docs/src/snippet.tsx").toString(),
    ),
    sourceReference: (source) =>
      `!!raw-loader!@site/${path.posix.relative("npm-packages/docs", source)}`,
  });
  process.stdout.write(JSON.stringify({ format: 1, ...result }));
  process.exit(0);
}
// The YAML lines between a plain `---` opening line and its closing delimiter
// line, as character offsets, or null for no frontmatter or another delimiter
// form. Annotations then have nowhere to live in that block.
function frontmatter(source, raw, offset) {
  if (offset === 0 || !source.startsWith("---") || source.slice(3, 3 + raw.length) !== raw) return null;
  const open = source.indexOf("\n");
  if (open < 0 || source.slice(3, open).trim() !== "") return null;
  const start = open + 1;
  const lines = raw.slice(raw.indexOf("\n") + 1);
  const end = lines === "" ? start : source.indexOf("\n", start + lines.length) + 1;
  const close = source.slice(end, offset).replace(/\r?\n$/, "");
  if (end <= 0 || end > offset || !/^---[ \t]*\r?$/.test(close)) return null;
  if (end - start > 64 * 1024) throw new Error("Frontmatter exceeds 64 KiB");
  return [start, end];
}

// A frontmatter comment is a whole line that gray-matter's own YAML engine
// ignores: blanking it, alone and together with every other such line, leaves
// the parsed value unchanged. Lines inside block scalars or quoted text change
// the value and are never comments. Returns [from, to) offsets from '#'.
function frontmatterComments(text) {
  const value = (lines) => {
    try {
      return JSON.stringify(yaml.safeLoad(lines.join("\n")) ?? null);
    } catch (error) {
      return undefined;
    }
  };
  const lines = text.split("\n");
  const expected = value(lines);
  const found = [];
  let cursor = 0;
  lines.forEach((line, index) => {
    const column = line.length - line.trimStart().length;
    if (line.trimStart().startsWith("#")) {
      if (value(lines.map((other, i) => (i === index ? "" : other))) === expected)
        found.push({ index, from: cursor + column, to: cursor + line.replace(/\r$/, "").length });
    }
    cursor += line.length + 1;
  });
  const indexes = new Set(found.map(({ index }) => index));
  if (found.length > 0 && value(lines.map((line, i) => (indexes.has(i) ? "" : line))) !== expected)
    throw new Error("Frontmatter comments change the parsed YAML");
  return found.map(({ from, to }) => [from, to]);
}

const files = request.files.map(({ path: name, source }) => {
  const result = {
    path: name,
    sha256: createHash("sha256").update(source).digest("hex"),
    comments: [],
    imports: [],
    error: "",
  };
  try {
    const parsed = matter(source, {
      engines: {
        javascript() {
          throw new Error("Executable frontmatter is not supported");
        },
      },
    });
    const { content } = parsed;
    const offset = source.length - content.length;
    const bytes = (index) => Buffer.byteLength(source.slice(0, index));
    const block = frontmatter(source, parsed.matter, offset);
    if (block) {
      const [start, end] = block;
      result.frontmatter = { start: bytes(start), end: bytes(end), text: source.slice(start, end) };
      for (const [from, to] of frontmatterComments(source.slice(start, end)))
        result.comments.push({
          start: bytes(start + from),
          end: bytes(start + to),
          text: source.slice(start + from, start + to),
        });
    }
    const format = name.endsWith(".mdx") ? "mdx" : "md";
    const tree = processors[format].parse(content);
    const range = (node) => {
      const start = offset + node.position.start.offset;
      const end = offset + node.position.end.offset;
      return {
        start: Buffer.byteLength(source.slice(0, start)),
        end: Buffer.byteLength(source.slice(0, end)),
        text: source.slice(start, end),
      };
    };
    function visit(node) {
      const hasFacts =
        node.type === "mdxFlowExpression" ||
        node.type === "html" ||
        node.type === "mdxjsEsm";
      const span = hasFacts && node.position && range(node);
      if (span && (node.type === "mdxFlowExpression" || node.type === "html")) {
        const match = (
          node.type === "html"
            ? /^<!--([\s\S]*?)-->$/
            : /^\{\/\*([\s\S]*?)\*\/\}$/
        ).exec(span.text);
        const before = source
          .slice(0, offset + node.position.start.offset)
          .split("\n")
          .at(-1);
        const after = source
          .slice(offset + node.position.end.offset)
          .split("\n")[0];
        if (match && !before.trim() && !after.trim()) {
          if (/^\s*concave:end\b/.test(match[1]) && span.text.includes("\n"))
            throw new Error("Concave end marker must occupy one line");
          result.comments.push(span);
        }
      }
      if (node.type === "mdxjsEsm") {
        for (const item of node.data.estree.body)
          if (item.type === "ImportDeclaration")
            result.imports.push({ ...span, specifier: item.source.value });
      }
      for (const child of node.children ?? []) visit(child);
    }
    visit(tree);
  } catch (error) {
    result.error = error.message;
  }
  return result;
});
process.stdout.write(JSON.stringify({ format: 1, files }));
