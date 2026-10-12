import * as zCore from "zod/v4/core";
import * as z from "zod/v4/mini";
import { describe, expect, expectTypeOf, test } from "vitest";
import {
  GenericValidator,
  v,
  VAny,
  VFloat64,
  VLiteral,
  VNull,
  VObject,
  VOptional,
  VString,
  VUnion,
} from "bijection/values";
import {
  zodToBijection,
  zid,
  zodToBijectionFields,
  zodOutputToBijectionFields,
} from "./zod4";
import type { Equals } from "../index.js";
import {
  assert,
  assertUnrepresentableType,
  ignoreUnionOrder,
  testZodOutputToBijection,
  testZodToBijection,
  testZodToBijectionInputAndOutput,
} from "./zod4.zodtoconvex.test";

describe("zodToBijection + zodOutputToBijection", () => {
  test("id", () => {
    testZodToBijectionInputAndOutput(zid("users"), v.id("users"));
  });
  test("string", () => testZodToBijectionInputAndOutput(z.string(), v.string()));
  test("string formatters", () =>
    testZodToBijectionInputAndOutput(z.email(), v.string()));
  test("number", () => testZodToBijectionInputAndOutput(z.number(), v.number()));
  test("float64", () => testZodToBijectionInputAndOutput(z.float64(), v.number()));
  test("nan", () => testZodToBijectionInputAndOutput(z.nan(), v.number()));
  test("int64", () => testZodToBijectionInputAndOutput(z.int64(), v.int64()));
  test("bigint", () => testZodToBijectionInputAndOutput(z.bigint(), v.int64()));
  test("boolean", () =>
    testZodToBijectionInputAndOutput(z.boolean(), v.boolean()));
  test("null", () => testZodToBijectionInputAndOutput(z.null(), v.null()));
  test("any", () => testZodToBijectionInputAndOutput(z.any(), v.any()));

  describe("literal", () => {
    test("string", () => {
      testZodToBijectionInputAndOutput(z.literal("hey"), v.literal("hey"));
    });
    test("number", () => {
      testZodToBijectionInputAndOutput(z.literal(42), v.literal(42));
    });
    test("int64", () => {
      testZodToBijectionInputAndOutput(z.literal(42n), v.literal(42n));
    });
    test("boolean", () => {
      testZodToBijectionInputAndOutput(z.literal(true), v.literal(true));
    });
    test("null", () => {
      testZodToBijectionInputAndOutput(z.literal(null), v.null()); // !
    });

    test("multiple values, same type", () => {
      testZodToBijectionInputAndOutput(
        z.literal([1, 2, 3]),
        ignoreUnionOrder(v.union(v.literal(1), v.literal(2), v.literal(3))),
      );
    });
    test("multiple values, different tyeps", () => {
      testZodToBijectionInputAndOutput(
        z.literal([123, "xyz", null]),
        ignoreUnionOrder(v.union(v.literal(123), v.literal("xyz"), v.null())),
      );
    });
    test("union of literals", () => {
      testZodToBijectionInputAndOutput(
        z.union([z.literal([1, 2]), z.literal([3, 4])]),
        v.union(
          ignoreUnionOrder(v.union(v.literal(1), v.literal(2))),
          ignoreUnionOrder(v.union(v.literal(3), v.literal(4))),
        ),
      );
    });
  });

  describe("optional", () => {
    test("z.optional()", () => {
      testZodToBijectionInputAndOutput(
        z.optional(z.string()),
        v.optional(v.string()),
      );
    });
    test("z.XYZ.optional()", () => {
      testZodToBijectionInputAndOutput(
        z.optional(z.string()),
        v.optional(v.string()),
      );
    });
    test("optional doesn’t propagate to array elements", () => {
      testZodToBijectionInputAndOutput(
        z.optional(z.array(z.number())),
        v.optional(v.array(v.number())), // and not v.optional(v.array(v.optional(v.number())))
      );
    });
  });

  test("array", () => {
    testZodToBijectionInputAndOutput(z.array(z.string()), v.array(v.string()));
  });

  describe("union", () => {
    test("never", () => {
      testZodToBijectionInputAndOutput(z.never(), v.union());
    });
    test("one element (number)", () => {
      testZodToBijectionInputAndOutput(z.union([z.number()]), v.union(v.number()));
    });
    test("one element (string)", () => {
      testZodToBijectionInputAndOutput(z.union([z.string()]), v.union(v.string()));
    });
    test("multiple elements", () => [
      testZodToBijectionInputAndOutput(
        z.union([z.string(), z.number()]),
        v.union(v.string(), v.number()),
      ),
    ]);
  });

  describe("brand", () => {
    test("string", () => {
      testZodToBijectionInputAndOutput(
        z.string().brand("myBrand"),
        v.string() as VString<string & zCore.$brand<"myBrand">>,
      );
    });
    test("number", () => {
      testZodToBijectionInputAndOutput(
        z.number().brand("myBrand"),
        v.number() as VFloat64<number & zCore.$brand<"myBrand">>,
      );
    });
    test("object", () => {
      testZodToBijectionInputAndOutput(
        z.object({ name: z.string() }).brand("myBrand"),
        v.object({ name: v.string() }) as VObject<
          {
            name: string;
          } & zCore.$brand<"myBrand">,
          {
            name: VString<string, "required">;
          },
          "required",
          "name"
        >,
      );
    });
  });

  test("object", () => {
    testZodToBijectionInputAndOutput(
      z.object({
        name: z.string(),
        age: z.number(),
        picture: z.optional(z.string()),
      }),

      // v.object() is a strict object, not a loose object,
      // but we still convert z.object() to it for convenience
      v.object({
        name: v.string(),
        age: v.number(),
        picture: v.optional(v.string()),
      }),
    );
  });

  test("strict object", () => {
    testZodToBijectionInputAndOutput(
      z.strictObject({
        name: z.string(),
        age: z.number(),
        picture: z.optional(z.string()),
      }),
      v.object({
        name: v.string(),
        age: v.number(),
        picture: v.optional(v.string()),
      }),
    );
  });

  describe("record", () => {
    test("key = string", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.string(), z.number()),
        v.record(v.string(), v.number()),
      );
    });

    test("key = string, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.string(), z.optional(z.number())),
        v.record(v.string(), v.number()),
      );
    });

    test("key = any", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.any(), z.number()),
        // v.record(v.any(), …) is not allowed in Convex validators
        v.record(v.string(), v.number()),
      );
    });

    test("key = any, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.any(), z.optional(z.number())),
        // v.record(v.any(), …) is not allowed in Convex validators
        v.record(v.string(), v.number()),
      );
    });

    test("key = literal", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.literal("user"), z.number()),
        // Convex records can’t have string literals as keys
        v.object({
          user: v.number(),
        }),
      );
    });

    test("key = literal, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.literal("user"), z.optional(z.number())),
        // Convex records can’t have string literals as keys
        v.object({
          user: v.optional(v.number()),
        }),
      );
    });

    test("key = literal with multiple values", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.literal(["user", "admin"]), z.number()),
        v.object({
          user: v.number(),
          admin: v.number(),
        }),
      );
    });

    test("key = literal with multiple values, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.literal(["user", "admin"]), z.optional(z.number())),
        v.object({
          user: v.optional(v.number()),
          admin: v.optional(v.number()),
        }),
      );
    });

    test("key = union of literals", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.union([z.literal("user"), z.literal("admin")]), z.number()),
        v.object({
          user: v.number(),
          admin: v.number(),
        }),
      );
    });

    test("key = union of literals, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(
          z.union([z.literal("user"), z.literal("admin")]),
          z.optional(z.number()),
        ),
        v.object({
          user: v.optional(v.number()),
          admin: v.optional(v.number()),
        }),
      );
    });

    test("key = union of literals with multiple values", () => {
      testZodToBijectionInputAndOutput(
        z.record(
          z.union([z.literal(["one", "two"]), z.literal(["three", "four"])]),
          z.number(),
        ),
        v.object({
          one: v.number(),
          two: v.number(),
          three: v.number(),
          four: v.number(),
        }),
      );
    });

    test("key = union of literals with multiple values, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(
          z.union([z.literal(["one", "two"]), z.literal(["three", "four"])]),
          z.optional(z.number()),
        ),
        v.object({
          one: v.optional(v.number()),
          two: v.optional(v.number()),
          three: v.optional(v.number()),
          four: v.optional(v.number()),
        }),
      );
    });

    test("key = v.id()", () => {
      {
        testZodToBijectionInputAndOutput(
          z.record(zid("documents"), z.number()),
          v.record(v.id("documents"), v.number()),
        );
      }
    });

    test("key = v.id(), optional", () => {
      {
        testZodToBijectionInputAndOutput(
          z.record(zid("documents"), z.optional(z.number())),
          v.record(v.id("documents"), v.number()),
        );
      }
    });

    test("key = union of ids", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.union([zid("users"), zid("documents")]), z.number()),
        v.record(v.union(v.id("users"), v.id("documents")), v.number()),
      );
    });

    test("key = union of ids, optional", () => {
      testZodToBijectionInputAndOutput(
        z.record(
          z.union([zid("users"), zid("documents")]),
          z.optional(z.number()),
        ),
        v.record(v.union(v.id("users"), v.id("documents")), v.number()),
      );
    });

    test("key = other", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.union([zid("users"), z.literal("none")]), z.number()),
        v.record(v.string(), v.number()),
      );
    });
  });

  describe("partial record", () => {
    test("key = any", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.any(), z.number()),
        // v.record(v.any(), …) is not allowed in Convex validators
        v.record(v.string(), v.number()),
      );
    });

    test("key = any, optional", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.any(), z.optional(z.number())),
        // v.record(v.any(), …) is not allowed in Convex validators
        v.record(v.string(), v.number()),
      );
    });

    test("key = string", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.string(), z.number()),
        v.record(v.string(), v.number()),
      );
    });

    test("key = string, optional", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.string(), z.optional(z.number())),
        v.record(v.string(), v.number()),
      );
    });

    test("key = literal", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.literal("user"), z.number()),
        // Convex records can’t have string literals as keys
        v.object({
          user: v.optional(v.number()),
        }),
      );
    });

    test("key = literal, optional", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.literal("user"), z.optional(z.number())),
        // Convex records can’t have string literals as keys
        v.object({
          user: v.optional(v.number()),
        }),
      );
    });

    test("key = union of literals", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(
          z.union([z.literal("user"), z.literal("admin")]),
          z.number(),
        ),
        v.object({
          user: v.optional(v.number()),
          admin: v.optional(v.number()),
        }),
      );
    });

    test("key = union of literals, optional", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(
          z.union([z.literal("user"), z.literal("admin")]),
          z.optional(z.number()),
        ),
        v.object({
          user: v.optional(v.number()),
          admin: v.optional(v.number()),
        }),
      );
    });

    test("key = v.id()", () => {
      {
        testZodToBijectionInputAndOutput(
          z.partialRecord(zid("documents"), z.number()),
          v.record(v.id("documents"), v.number()),
        );
      }
    });

    test("key = v.id(), optional", () => {
      {
        testZodToBijectionInputAndOutput(
          z.partialRecord(zid("documents"), z.optional(z.number())),
          v.record(v.id("documents"), v.number()),
        );
      }
    });

    test("key = union of ids", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(z.union([zid("users"), zid("documents")]), z.number()),
        v.record(v.union(v.id("users"), v.id("documents")), v.number()),
      );
    });

    test("key = union of ids, optional", () => {
      testZodToBijectionInputAndOutput(
        z.partialRecord(
          z.union([zid("users"), zid("documents")]),
          z.optional(z.number()),
        ),
        v.record(v.union(v.id("users"), v.id("documents")), v.number()),
      );
    });

    test("key = other", () => {
      testZodToBijectionInputAndOutput(
        z.record(z.union([zid("users"), z.literal("none")]), z.number()),
        v.record(v.string(), v.number()),
      );
    });
  });

  test("readonly", () => {
    testZodToBijectionInputAndOutput(
      z.readonly(z.array(z.string())),
      v.array(v.string()),
    );
  });

  // Discriminated union
  test("discriminated union", () => {
    testZodToBijectionInputAndOutput(
      z.discriminatedUnion("status", [
        z.object({ status: z.literal("success"), data: z.string() }),
        z.object({ status: z.literal("failed"), error: z.string() }),
      ]),
      v.union(
        v.object({ status: v.literal("success"), data: v.string() }),
        v.object({ status: v.literal("failed"), error: v.string() }),
      ),
    );
  });

  describe("enum", () => {
    test("const array", () => {
      testZodToBijectionInputAndOutput(
        z.enum(["Salmon", "Tuna", "Trout"]),
        ignoreUnionOrder(
          v.union(v.literal("Salmon"), v.literal("Tuna"), v.literal("Trout")),
        ),
      );
    });

    test("const array with a number", () => {
      testZodToBijectionInputAndOutput(
        z.enum(["2", "Salmon", "Tuna"]),
        ignoreUnionOrder(
          v.union(v.literal("2"), v.literal("Salmon"), v.literal("Tuna")),
        ),
      );
    });

    test("enum-like object literal", () => {
      const Fish = {
        Salmon: 0,
        Tuna: 1,
      } as const;
      testZodToBijectionInputAndOutput(
        z.enum(Fish),
        ignoreUnionOrder(v.union(v.literal(0), v.literal(1))),
      );
    });

    test("TypeScript string enum", () => {
      enum Fish {
        Salmon = 0,
        Tuna = 1,
      }

      testZodToBijectionInputAndOutput(
        z.enum(Fish),
        // Interestingly, TypeScript enums make Fish.Salmon be its own type,
        // even if its value is 0 at runtime.
        ignoreUnionOrder(v.union(v.literal(Fish.Salmon), v.literal(Fish.Tuna))),
      );
    });
  });

  // Tuple
  describe("tuple", () => {
    test("one-element tuple", () => {
      testZodToBijectionInputAndOutput(
        z.tuple([z.string()]),
        v.array(v.union(v.string())), // suboptimal, we could remove the union
      );
    });
    test("fixed elements, same type", () => {
      testZodToBijectionInputAndOutput(
        z.tuple([z.string(), z.string()]),
        v.array(v.union(v.string(), v.string())), // suboptimal, we could remove duplicates
      );
    });
    test("fixed elements", () => {
      testZodToBijectionInputAndOutput(
        z.tuple([z.string(), z.number()]),
        v.array(v.union(v.string(), v.number())),
      );
    });
    test("variadic element, same type", () => {
      testZodToBijectionInputAndOutput(
        z.tuple([z.string()], z.string()),
        v.array(v.union(v.string(), v.string())), // suboptimal, we could remove duplicates
      );
    });
    test("variadic element", () => {
      testZodToBijectionInputAndOutput(
        z.tuple([z.string()], z.number()),
        v.array(v.union(v.string(), v.number())),
      );
    });
  });

  describe("nullable", () => {
    test("nullable(string)", () => {
      testZodToBijectionInputAndOutput(
        z.nullable(z.string()),
        v.union(v.string(), v.null()),
      );
    });
    test("nullable(number)", () => {
      testZodToBijectionInputAndOutput(
        z.nullable(z.number()),
        v.union(v.number(), v.null()),
      );
    });
    test("optional(nullable(string))", () => {
      testZodToBijectionInputAndOutput(
        z.optional(z.nullable(z.string())),
        v.optional(v.union(v.string(), v.null())),
      );

      zodToBijection(z.optional(z.nullable(z.string()))) satisfies VUnion<
        string | null | undefined,
        [VString, VNull],
        "optional"
      >;
    });
    test("nullable(optional(string)) → swap nullable and optional", () => {
      testZodToBijectionInputAndOutput(
        z.optional(z.nullable(z.string())),
        v.optional(v.union(v.string(), v.null())),
      );

      zodToBijection(z.optional(z.nullable(z.string()))) satisfies VUnion<
        string | null | undefined,
        [VString, VNull],
        "optional"
      >;
    });
  });

  test("optional", () => {
    testZodToBijectionInputAndOutput(
      z.optional(z.string()),
      v.optional(v.string()),
    );
  });

  describe("non-optional", () => {
    test("id", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(zid("documents"))),
        v.id("documents"),
      );
    });
    test("string", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.string())),
        v.string(),
      );
    });
    test("float64", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.float64())),
        v.float64(),
      );
    });
    test("int64", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.int64())),
        v.int64(),
      );
    });
    test("boolean", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.boolean())),
        v.boolean(),
      );
    });
    test("null", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.null())),
        v.null(),
      );
    });
    test("any", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.any())),
        v.any(),
      );
    });
    test("literal", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.literal(42n))),
        v.literal(42n),
      );
    });
    test("object", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(
          z.optional(
            z.object({
              required: z.string(),
              optional: z.optional(z.number()),
            }),
          ),
        ),
        v.object({ required: v.string(), optional: v.optional(v.number()) }),
      );
    });
    test("array", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.array(z.int64()))),
        v.array(v.int64()),
      );
    });
    test("record", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.record(z.string(), z.number()))),
        v.record(v.string(), v.number()),
      );
    });
    test("union", () => {
      testZodToBijectionInputAndOutput(
        z.nonoptional(z.optional(z.union([z.number(), z.string()]))),
        v.union(v.number(), v.string()),
      );
    });

    test("nonoptional on non-optional type", () => {
      testZodToBijectionInputAndOutput(z.nonoptional(z.string()), v.string());
    });
  });

  test("lazy", () => {
    testZodToBijectionInputAndOutput(
      z.lazy(() => z.string()),
      v.string(),
    );
  });

  test("custom", () => {
    testZodToBijectionInputAndOutput(
      z.custom<string & { __myBrand: true }>(() => true),
      v.any(),
    );
  });

  test("recursive type", () => {
    // Cast through `as zCore.$ZodType` to keep the Type slot opaque: a
    // self-referential schema makes `BijectionValidatorFromZod` circularly
    // reference itself, a recoverable TS2615. Compile-time shape verification
    // is covered by the non-recursive tests; this only asserts the runtime
    // conversion produces the right validator.
    const category = z.object({
      name: z.string(),
      get subcategories() {
        return z.array(category);
      },
    });

    testZodToBijectionInputAndOutput(
      category,
      // @ts-expect-error -- the TypeScript type is recursive, while the
      // runtime time uses `v.any()` since Convex validators can’t be recursive
      v.object({
        name: v.string(),
        subcategories: v.array(v.any()),
      }),
    );
  });

  test("catch", () => {
    testZodToBijectionInputAndOutput(z.catch(z.string(), "hello"), v.string());
  });

  describe("template literals", () => {
    test("constant string", () => {
      testZodToBijectionInputAndOutput(
        z.templateLiteral(["hi there"]),
        v.string() as VString<"hi there", "required">,
      );
    });
    test("string interpolation", () => {
      testZodToBijectionInputAndOutput(
        z.templateLiteral(["email: ", z.string()]),
        v.string() as VString<`email: ${string}`, "required">,
      );
    });
    test("literal interpolation", () => {
      testZodToBijectionInputAndOutput(
        z.templateLiteral(["high", z.literal(5)]),
        v.string() as VString<"high5", "required">,
      );
    });
    test("nullable interpolation", () => {
      testZodToBijectionInputAndOutput(
        z.templateLiteral([z.nullable(z.literal("grassy"))]),
        v.string() as VString<"grassy" | "null", "required">,
      );
    });
    test("enum interpolation", () => {
      testZodToBijectionInputAndOutput(
        z.templateLiteral([z.number(), z.enum(["px", "em", "rem"])]),
        v.string() as VString<`${number}${"px" | "em" | "rem"}`, "required">,
      );
    });
  });

  test("intersection", () => {
    // We could do some more advanced logic here where we compute
    // the Convex validator that results from the intersection.
    // For now, we simply use v.any()
    testZodToBijectionInputAndOutput(
      z.intersection(
        z.object({ key1: z.string() }),
        z.object({ key2: z.string() }),
      ),
      v.any(),
    );
  });

  describe("unencodable types", () => {
    test("z.date", () => {
      assertUnrepresentableType(z.date());
    });
    test("z.symbol", () => {
      assertUnrepresentableType(z.symbol());
    });
    test("z.map", () => {
      assertUnrepresentableType(z.map(z.string(), z.string()));
    });
    test("z.set", () => {
      assertUnrepresentableType(z.set(z.string()));
    });
    test("z.promise", () => {
      assertUnrepresentableType(z.promise(z.string()));
    });
    test("z.file", () => {
      assertUnrepresentableType(z.file());
    });
    test("z.function", () => {
      assertUnrepresentableType(z.function());
    });
    test("z.void", () => {
      assertUnrepresentableType(z.void());
    });
    test("z.undefined", () => {
      assertUnrepresentableType(z.undefined());
    });
    test("z.literal(undefined)", () => {
      assertUnrepresentableType(z.literal(undefined));
    });
    test("z.literal including undefined", () => {
      assertUnrepresentableType(z.literal([123, undefined]));
    });
  });
});

describe("zodToBijection", () => {
  test("transform", () => {
    testZodOutputToBijection(
      z.transform((s: number) => s.toString()),
      v.any(), // input type unknown here
    );
  });

  test("pipe", () => {
    testZodToBijection(
      z.pipe(
        z.number(),
        z.transform((s) => s.toString()),
      ),
      v.number(), // input type
    );
  });

  test("codec", () => {
    testZodToBijection(
      z.codec(z.string(), z.number(), {
        decode: (s: string) => parseInt(s, 10),
        encode: (n: number) => n.toString(),
      }),
      v.string(), // input type
    );
  });

  test("default", () => {
    testZodToBijection(z._default(z.string(), "hello"), v.optional(v.string()));
  });

  describe("problematic inputs", () => {
    test("unknown", () => {
      const someType: unknown = z.string();
      const _asBijection = zodToBijection(
        // @ts-expect-error Can’t use unknown
        someType,
      );
      assert<Equals<typeof _asBijection, GenericValidator>>();
    });

    test("ZodType<unknown>", () => {
      const someType: zCore.$ZodType<unknown> = z.string();
      const _asBijection = zodToBijection(someType);
      assert<Equals<typeof _asBijection, GenericValidator>>();
    });

    test("ZodType<some>", () => {
      const someType: zCore.$ZodType<string> = z.string();
      const _asBijection = zodToBijection(someType);
      assert<Equals<typeof _asBijection, GenericValidator>>();
    });

    test("any type", () => {
      const someType: any = z.string();
      const _asBijection = zodToBijection(someType);
      assert<Equals<typeof _asBijection, GenericValidator>>();
    });
  });

  describe("lazy", () => {
    test("throwing", () => {
      expect(() =>
        zodToBijection(
          z.lazy((): zCore.$ZodString => {
            throw new Error("This shouldn’t throw but it did");
          }),
        ),
      ).toThrowError("This shouldn’t throw but it did");
    });
  });
});

describe("zodOutputToBijection", () => {
  test("transform", () => {
    testZodOutputToBijection(
      z.transform((s: number) => s.toString()),
      v.any(), // this transform doesn’t hold runtime info about the output type
    );
  });

  test("pipe", () => {
    testZodOutputToBijection(
      z.pipe(
        z.number(),
        z.transform((s) => s.toString()),
      ),
      v.any(), // this transform doesn’t hold runtime info about the output type
    );
  });

  describe("codec", () => {
    test("simple case", () => {
      testZodOutputToBijection(
        z.codec(z.string(), z.number(), {
          decode: (s: string) => parseInt(s, 10),
          encode: (n: number) => n.toString(),
        }),
        v.number(), // output type
      );
    });

    describe("with optional/nullable", () => {
      // Codec that transforms Date to milliseconds (number)
      const dateToMsCodec = z.codec(z.date(), z.int().check(z.gte(10)), {
        encode: (millis: number) => new Date(millis),
        decode: (date: Date) => date.getTime(),
      });

      test("optional codec returns VOptional<VFloat64>", () => {
        testZodOutputToBijection(
          z.optional(dateToMsCodec),
          v.optional(v.number()),
        );
      });

      test("nullable codec returns VUnion with null", () => {
        testZodOutputToBijection(
          z.nullable(dateToMsCodec),
          v.union(v.number(), v.null()),
        );
      });

      test("optional nullable codec", () => {
        testZodOutputToBijection(
          z.optional(z.nullable(dateToMsCodec)),
          v.optional(v.union(v.number(), v.null())),
        );
      });

      test("nullable optional codec (swapped order)", () => {
        testZodOutputToBijection(
          z.nullable(z.optional(dateToMsCodec)),
          v.optional(v.union(v.number(), v.null())),
        );
      });

      // String to number codec
      const stringToNumberCodec = z.codec(z.string(), z.number(), {
        decode: (s: string) => parseFloat(s),
        encode: (n: number) => n.toString(),
      });

      test("optional string-to-number codec", () => {
        testZodOutputToBijection(
          z.optional(stringToNumberCodec),
          v.optional(v.number()),
        );
      });

      test("codec with default", () => {
        testZodOutputToBijection(
          z._default(dateToMsCodec, Date.now()),
          v.number(), // default means output is always present
        );
      });
    });
  });

  test("default", () => {
    testZodOutputToBijection(z._default(z.string(), "hello"), v.string());
  });
});

describe("codec or pipe nested in another schema", () => {
  // A nested schema is converted from the same side as the schema containing
  // it: the codec’s input (a string) in zodToBijection, and its output (a number)
  // in zodOutputToBijection.
  const stringToNumberCodec = z.codec(z.string(), z.number(), {
    decode: (s: string) => parseFloat(s),
    encode: (n: number) => n.toString(),
  });

  test("object field", () => {
    const schema = z.object({ n: stringToNumberCodec });
    testZodOutputToBijection(schema, v.object({ n: v.number() }));

    // The object type of zodToBijection comes from z.infer, so only the field is compared
    const inputValidator = zodToBijection(schema);
    expectTypeOf(inputValidator.fields.n).toEqualTypeOf<VString>();
    expect(inputValidator).toEqual(v.object({ n: v.string() }));
  });

  test("optional object field", () => {
    testZodOutputToBijection(
      z.object({ n: z.optional(stringToNumberCodec) }),
      v.object({ n: v.optional(v.number()) }),
    );
  });

  test("object field with a default", () => {
    testZodOutputToBijection(
      z.object({ n: z._default(stringToNumberCodec, 0) }),
      v.object({ n: v.number() }), // default means output is always present
    );
  });

  test("array element", () => {
    testZodToBijection(z.array(stringToNumberCodec), v.array(v.string()));
    testZodOutputToBijection(z.array(stringToNumberCodec), v.array(v.number()));
  });

  test("nullable array element", () => {
    testZodToBijection(
      z.array(z.nullable(stringToNumberCodec)),
      v.array(v.union(v.string(), v.null())),
    );
    testZodOutputToBijection(
      z.array(z.nullable(stringToNumberCodec)),
      v.array(v.union(v.number(), v.null())),
    );
  });

  test("object in an array", () => {
    testZodOutputToBijection(
      z.array(z.object({ n: stringToNumberCodec })),
      v.array(v.object({ n: v.number() })),
    );
  });

  test("pipe from a transform, in an array", () => {
    const schema = z.array(
      z.pipe(
        z.transform((value: unknown) => value),
        z.object({ n: z.number() }),
      ),
    );
    testZodToBijection(schema, v.array(v.any())); // the input is a transform
    testZodOutputToBijection(schema, v.array(v.object({ n: v.number() })));
  });

  test("union member", () => {
    testZodToBijection(
      z.union([stringToNumberCodec, z.null()]),
      v.union(v.string(), v.null()),
    );
    testZodOutputToBijection(
      z.union([stringToNumberCodec, z.null()]),
      v.union(v.number(), v.null()),
    );
  });

  test("tuple item", () => {
    testZodToBijection(
      z.tuple([stringToNumberCodec]),
      v.array(v.union(v.string())),
    );
    testZodOutputToBijection(
      z.tuple([stringToNumberCodec]),
      v.array(v.union(v.number())),
    );
  });

  test("record value", () => {
    const schema = z.record(z.string(), stringToNumberCodec);
    testZodOutputToBijection(schema, v.record(v.string(), v.number()));

    // The record type of zodToBijection comes from z.infer, so only the value is compared
    const inputValidator = zodToBijection(schema);
    expectTypeOf(inputValidator.value).toEqualTypeOf<VString>();
    expect(inputValidator).toEqual(v.record(v.string(), v.string()));
  });

  test("record value, key = literal", () => {
    testZodToBijection(
      z.record(z.literal(["a", "b"]), stringToNumberCodec),
      v.object({ a: v.string(), b: v.string() }),
    );
    testZodOutputToBijection(
      z.record(z.literal(["a", "b"]), stringToNumberCodec),
      v.object({ a: v.number(), b: v.number() }),
    );
  });

  test("readonly", () => {
    testZodToBijection(z.readonly(stringToNumberCodec), v.string());
    testZodOutputToBijection(z.readonly(stringToNumberCodec), v.number());
  });

  test("lazy", () => {
    testZodToBijection(
      z.lazy(() => stringToNumberCodec),
      v.string(),
    );
    testZodOutputToBijection(
      z.lazy(() => stringToNumberCodec),
      v.number(),
    );
  });

  test("catch", () => {
    testZodToBijection(z.catch(stringToNumberCodec, 0), v.string());
    testZodOutputToBijection(z.catch(stringToNumberCodec, 0), v.number());
  });

  test("non-optional", () => {
    testZodToBijection(z.nonoptional(z.optional(stringToNumberCodec)), v.string());
    testZodOutputToBijection(
      z.nonoptional(z.optional(stringToNumberCodec)),
      v.number(),
    );
  });
});

test("zodToBijectionFields", () => {
  const convexFields = zodToBijectionFields({
    name: z.string(),
    age: z.optional(z.number()),
    transform: z.pipe(
      z.number(),
      z.transform((z) => z.toString()),
    ),
  });

  assert<
    Equals<
      typeof convexFields,
      {
        name: VString;
        age: VOptional<VFloat64>;
        transform: VFloat64;
      }
    >
  >();

  expect(convexFields).toEqual({
    name: v.string(),
    age: v.optional(v.number()),
    transform: v.number(),
  });
});

test("zodOutputToBijectionFields", () => {
  const convexFields = zodOutputToBijectionFields({
    name: z.string(),
    age: z.optional(z.number()),
    transform: z.pipe(
      z.number(),
      z.transform((z) => z.toString()),
    ),
  });

  assert<
    Equals<
      typeof convexFields,
      {
        name: VString;
        age: VOptional<VFloat64>;
        transform: VAny;
      }
    >
  >();

  expect(convexFields).toEqual({
    name: v.string(),
    age: v.optional(v.number()),
    transform: v.any(),
  });
});

describe("testing infrastructure", () => {
  test("test methods don’t typecheck if the IsOptional value of the result isn’t set correctly", () => {
    // eslint-disable-next-line no-constant-condition
    if (false) {
      // typecheck only
      testZodToBijection(
        z.string(),
        // @ts-expect-error -- This error should be caught by TypeScript
        v.optional(v.string()),
      );
      testZodToBijection(
        z.optional(z.string()),
        // @ts-expect-error -- This error should be caught by TypeScript
        v.string(),
      );

      testZodOutputToBijection(
        z.string(),
        // @ts-expect-error -- This error should be caught by TypeScript
        v.optional(v.string()),
      );
      testZodOutputToBijection(
        z.optional(z.string()),
        // @ts-expect-error -- This error should be caught by TypeScript
        v.string(),
      );

      testZodToBijectionInputAndOutput(
        z.string(),
        // @ts-expect-error -- This error should be caught by TypeScript
        v.optional(v.string()),
      );
      testZodToBijectionInputAndOutput(
        z.optional(z.string()),
        // @ts-expect-error -- This error should be caught by TypeScript
        v.string(),
      );
    }
  });

  test("test methods typecheck if the IsOptional value of the result is set correctly", () => {
    testZodToBijection(z.optional(z.string()), v.optional(v.string()));
    testZodToBijection(z.string(), v.string());

    testZodOutputToBijection(z.optional(z.string()), v.optional(v.string()));
    testZodOutputToBijection(z.string(), v.string());

    testZodToBijectionInputAndOutput(
      z.optional(z.string()),
      v.optional(v.string()),
    );
    testZodToBijectionInputAndOutput(z.string(), v.string());
  });

  test("removeUnionOrder", () => {
    const unionWithOrder = v.union(v.literal(1), v.literal(2), v.literal(3));
    assert<
      Equals<
        typeof unionWithOrder,
        VUnion<
          1 | 2 | 3,
          [
            VLiteral<1, "required">,
            VLiteral<2, "required">,
            VLiteral<3, "required">,
          ],
          "required",
          never
        >
      >
    >();

    const _unionWithoutOrder = ignoreUnionOrder(unionWithOrder);
    assert<
      Equals<
        typeof _unionWithoutOrder,
        VUnion<
          1 | 2 | 3,
          (
            | VLiteral<1, "required">
            | VLiteral<2, "required">
            | VLiteral<3, "required">
          )[],
          "required",
          never
        >
      >
    >();
  });

  test("assertUnrepresentableType", () => {
    expect(() => {
      assertUnrepresentableType(z.string());
    }).toThrowError();
  });
});
