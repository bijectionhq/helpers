import path from "node:path";
import { createRequire } from "node:module";

export async function captureRuntime(req, input, source, entries) {
  const { read, absolute } = source;
  const esbuild = req("esbuild");
  const esbuildVersion = esbuild.version;
  const esbuildPath = req.resolve("esbuild");
  read(esbuildPath);
  read(
    process.env.ESBUILD_BINARY_PATH ||
      createRequire(esbuildPath).resolve(
        `@esbuild/${process.platform}-${process.arch}/${process.platform === "win32" ? "esbuild.exe" : "bin/esbuild"}`,
      ),
  );
  for (const file of input.entries) {
    const result = await esbuild.build({
      entryPoints: [absolute(file)],
      bundle: true,
      write: false,
      format: "esm",
      platform: "neutral",
      metafile: true,
      logLevel: "silent",
      outdir: "out",
      ...(input.project ? { tsconfig: absolute(input.project) } : {}),
      plugins: [
        {
          name: "concave-inputs",
          setup(build) {
            build.onResolve({ filter: /^[^./]/ }, (args) => ({
              path: args.path,
              external: true,
            }));
            build.onLoad({ filter: /\.(?:[cm]?[jt]sx?|json)$/ }, (args) => {
              const contents = read(args.path);
              const ext = path.extname(args.path).slice(1);
              const loader =
                { mts: "ts", cts: "ts", mjs: "js", cjs: "js" }[ext] || ext;
              return { contents, loader };
            });
          },
        },
      ],
    });
    const outputs = Object.values(result.metafile.outputs).filter(
      (o) => o.entryPoint,
    );
    if (outputs.length !== 1)
      throw new Error("Expected one runtime export output");
    entries[file].runtime = [...outputs[0].exports].sort();
  }
  return esbuildVersion;
}
