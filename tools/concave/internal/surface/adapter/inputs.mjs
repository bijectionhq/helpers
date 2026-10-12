import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";

export class StaleInput extends Error {}

export const digest = (data) => crypto.createHash("sha256").update(data).digest("hex");

// Every positive read and negative lookup carries the same state schema as Go.
// Go performs the final freshness sweep after receiving this response.
export function captureInputs(root) {
  const absolute = (file) => path.resolve(root, file);
  const relative = (file) =>
    path.relative(root, file).split(path.sep).join("/") || ".";
  const inputs = new Map();
  let bytes = 0;
  function read(file) {
    file = absolute(file);
    let state = null,
      data,
      descriptor;
    try {
      const before = fs.statSync(file);
      if (before.isDirectory()) state = { kind: "directory" };
      else {
        if (!before.isFile() || before.size > 16 * 1024 * 1024)
          throw new Error(`Unsupported compiler input: ${relative(file)}`);
        descriptor = fs.openSync(file, "r");
        const buffer = Buffer.alloc(before.size + 1);
        let length = 0,
          count;
        while (
          length < buffer.length &&
          (count = fs.readSync(
            descriptor,
            buffer,
            length,
            buffer.length - length,
            null,
          ))
        )
          length += count;
        const after = fs.fstatSync(descriptor);
        if (
          length !== before.size ||
          after.ino !== before.ino ||
          after.dev !== before.dev ||
          after.size !== before.size ||
          after.mtimeMs !== before.mtimeMs
        )
          throw new StaleInput(
            `Source changed while reading: ${relative(file)}`,
          );
        data = buffer.subarray(0, length);
        state = { kind: "file", sha256: digest(data), size: data.length };
      }
    } catch (error) {
      if (error.code !== "ENOENT" && error.code !== "ENOTDIR") throw error;
    } finally {
      if (descriptor !== undefined) fs.closeSync(descriptor);
    }
    const previous = inputs.get(file);
    if (
      inputs.has(file) &&
      (previous?.kind !== state?.kind ||
        previous?.sha256 !== state?.sha256 ||
        previous?.size !== state?.size)
    )
      throw new StaleInput(`Source changed: ${relative(file)}`);
    if (!inputs.has(file)) {
      bytes += data?.length ?? 0;
      if (inputs.size >= 40000 || bytes > 256 * 1024 * 1024)
        throw new Error("TypeScript input bound exceeded");
    }
    inputs.set(file, state);
    return data?.toString("utf8");
  }
  function exists(file) {
    try {
      if (fs.statSync(absolute(file)).isFile()) return true;
    } catch (error) {
      if (error.code !== "ENOENT" && error.code !== "ENOTDIR") throw error;
    }
    read(file);
    return false;
  }
  function directoryExists(file) {
    // Directory existence affects module resolution, including negative lookups.
    read(file);
    return inputs.get(absolute(file))?.kind === "directory";
  }
  return {
    root,
    absolute,
    relative,
    read,
    exists,
    directoryExists,
    manifest: () =>
      Object.fromEntries(
        [...inputs]
          .map(([file, state]) => [relative(file), state])
          .sort(([a], [b]) => a.localeCompare(b)),
      ),
  };
}
