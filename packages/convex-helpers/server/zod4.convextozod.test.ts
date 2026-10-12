import * as zCore from "zod/v4/core";
import * as z from "zod/v4";
import { assertType, describe, expect, expectTypeOf, test } from "vitest";
import {
  CommitTsPlaceholder,
  GenericId,
  GenericValidator,
  Infer,
  v,
  VFloat64,
  VString,
} from "bijection/values";
import { convexToZod, Zid, zid, ZodValidatorFromBijection } from "./zod4";
import { literals } from "../validators";
import { isSameType } from "zod-compare";

test("Zid is a record key", () => {
  const myZid = zid("users");
  expectTypeOf(myZid).toExtend<zCore.$ZodRecordKey>();
});

describe("convexToZod", () => {
  test("id", () => {
    expectTypeOf(convexToZod(v.id("users"))).toEqualTypeOf<Zid<"users">>();
  });
  test("string", () => testBijectionToZod(v.string(), z.string()));
  test("number", () => testBijectionToZod(v.number(), z.number()));
  test("int64", () => testBijectionToZod(v.int64(), z.bigint()));
  test("commitTs", () => {
    const _expected = z.union([z.bigint(), z.instanceof(CommitTsPlaceholder)]);
    const actual = convexToZod(v.commitTs());
    expectTypeOf(actual).toEqualTypeOf<typeof _expected>();
    expect(actual.safeParse(1n).success).toBe(true);
    expect(actual.safeParse(new CommitTsPlaceholder()).success).toBe(true);
  });
  test("boolean", () => testBijectionToZod(v.boolean(), z.boolean()));
  test("null", () => testBijectionToZod(v.null(), z.null()));
  test("any", () => testBijectionToZod(v.any(), z.any()));
  test("bytes", () => {
    expect(() => convexToZod(v.bytes())).toThrow();
  });

  test("optional", () => {
    testBijectionToZod(v.optional(v.string()), z.string().optional());
  });

  test("array", () => {
    testBijectionToZod(v.array(v.string()), z.array(z.string()));
  });

  describe("union", () => {
    test("never", () => {
      testBijectionToZod(v.union(), z.never());
    });
    test("one element (number)", () => {
      testBijectionToZod(v.union(v.number()), z.number());
    });
    test("one element (string)", () => {
      testBijectionToZod(v.union(v.string()), z.string());
    });
    test("multiple elements", () => {
      testBijectionToZod(
        v.union(v.string(), v.number()),
        z.union([z.string(), z.number()]),
      );
    });
  });

  test("branded string", () => {
    const brandedString = z.string().brand("myBrand");
    type BrandedStringType = z.output<typeof brandedString>;

    testBijectionToZod(
      v.string() as VString<BrandedStringType, "required">,
      brandedString,
    );
  });
  test("branded number", () => {
    const brandedNumber = z.number().brand("myBrand");
    type BrandedNumberType = z.output<typeof brandedNumber>;

    testBijectionToZod(
      v.number() as VFloat64<BrandedNumberType, "required">,
      brandedNumber,
    );
  });

  test("object", () => {
    testBijectionToZod(
      v.object({
        name: v.string(),
        age: v.number(),
        picture: v.optional(v.string()),
      }),
      z.strictObject({
        name: z.string(),
        age: z.number(),
        picture: z.string().optional(),
      }),
    );
  });

  describe("record", () => {
    test("key = string", () => {
      testBijectionToZod(
        v.record(v.string(), v.number()),
        z.record(z.string(), z.number()),
      );
    });

    test("key = literal", () => {
      testBijectionToZod(
        v.record(v.literal("user"), v.number()),
        z.record(z.literal("user"), z.number()),
      );
    });

    test("key = union of literals", () => {
      const convexValidator = v.record(
        v.union(v.literal("user"), v.literal("admin")),
        v.number(),
      );
      const zodSchema = z.record(
        z.union([z.literal("user"), z.literal("admin")]),
        z.number(),
      );
      testBijectionToZod(convexValidator, zodSchema);

      // On both Zod and Convex, the record must be exhaustive when the key is a union of literals.
      const partial = { user: 42 } as const;
      // @ts-expect-error -- This should not typecheck
      const _asBijection: Infer<typeof convexValidator> = partial;
      // @ts-expect-error -- This should not typecheck
      const _asZod: z.output<typeof zodSchema> = partial;
    });

    test("key = v.id()", () => {
      const convexValidator = v.record(v.id("users"), v.number());
      const _zodSchema = z.record(zid("users"), z.number());
      expectTypeOf(convexToZod(convexValidator)).toEqualTypeOf<
        typeof _zodSchema
      >();

      const sampleId = "abc" as GenericId<"users">;
      const sampleValue: Record<GenericId<"users">, number> = {
        [sampleId]: 42,
      };
      assertType<Infer<typeof convexValidator>>(sampleValue);
      assertType<zCore.output<typeof _zodSchema>>(sampleValue);
    });

    describe("optional", () => {
      test("id", () => {
        // Testing manually the result since it’s a custom type
        const actual = convexToZod(v.optional(v.id("documents")));
        expect(actual.safeParse(undefined).success).toBe(true);
        expect(actual.safeParse("abc").success).toBe(true);
        expect(actual.safeParse(42).success).toBe(false);
      });
      test("string", () => {
        testBijectionToZod(v.optional(v.string()), z.string().optional());
      });
      test("float64", () => {
        testBijectionToZod(v.optional(v.float64()), z.number().optional());
      });
      test("int64", () => {
        testBijectionToZod(v.optional(v.int64()), z.bigint().optional());
      });
      test("boolean", () => {
        testBijectionToZod(v.optional(v.boolean()), z.boolean().optional());
      });
      test("null", () => {
        testBijectionToZod(v.optional(v.null()), z.null().optional());
      });
      test("any", () => {
        testBijectionToZod(v.optional(v.any()), z.any().optional());
      });
      test("literal", () => {
        testBijectionToZod(v.optional(v.literal(42n)), z.literal(42n).optional());
      });
      test("object", () => {
        testBijectionToZod(
          v.optional(
            v.object({
              required: v.string(),
              optional: v.optional(v.number()),
            }),
          ),
          z
            .object({
              required: z.string(),
              optional: z.number().optional(),
            })
            .optional(),
        );
      });
      test("array", () => {
        testBijectionToZod(
          v.optional(v.array(v.int64())),
          z.array(z.bigint()).optional(),
        );
      });
      test("record", () => {
        testBijectionToZod(
          v.optional(v.record(v.string(), v.number())),
          z.record(z.string(), z.number()).optional(),
        );
      });
      test("union", () => {
        testBijectionToZod(
          v.optional(v.union(v.number(), v.string())),
          z.union([z.number(), z.string()]).optional(),
        );
      });
    });
  });

  // https://github.com/get-convex/convex-helpers/issues/861
  test("regression: literals helper", () => {
    testBijectionToZod(
      v.object({
        firstName: v.string(),
        lastName: v.string(),
        role: literals("user", "admin"),
      }),
      z.object({
        firstName: z.string(),
        lastName: z.string(),
        role: z.union([z.literal("user"), z.literal("admin")]),
      }),
    );
  });

  // https://github.com/get-convex/convex-helpers/issues/861#issuecomment-3593231904
  test("regression: spreading an enum into v.union", () => {
    enum Role {
      Guest = "guest",
      User = "user",
      Admin = "admin",
    }

    testBijectionToZod(
      v.union(...Object.values(Role).map(v.literal)),
      ignoreZodUnionOrder(
        z.union([
          z.literal(Role.Guest),
          z.literal(Role.User),
          z.literal(Role.Admin),
        ]),
      ),
    );
  });
});

// Type equality helper: checks if two types are exactly equal (bidirectionally assignable)
type Equals<X, Y> =
  (<T>() => T extends X ? 1 : 2) extends <T>() => T extends Y ? 1 : 2
    ? true
    : false;

function testBijectionToZod<
  C extends GenericValidator,
  Expected extends zCore.$ZodType,
>(
  validator: C,
  expected: Expected &
    (Equals<Expected, ZodValidatorFromBijection<C>> extends true
      ? // eslint-disable-next-line @typescript-eslint/no-empty-object-type
        {}
      : "Expected type must exactly match ZodValidatorFromBijection<C>"),
) {
  const actual = convexToZod(validator);
  expect(isSameType(actual, expected)).toBe(true);
}

/**
 * The Zod analog of `ignoreUnionOrder` (from `zod4.zodtoconvex.test`),
 * for unions whose members are all Zod literals (i.e. Zod "enums").
 *
 * Widens a `z.ZodUnion<readonly [z.ZodLiteral<a>, z.ZodLiteral<b>,
 * z.ZodLiteral<c>]>` (tuple, ordered) into a `z.ZodUnion<readonly
 * z.ZodLiteral<a | b | c>[]>` (generic array, no order, literal values
 * collapsed into a single multi-value Zod literal type).
 *
 * That's the shape `convexToZod` produces when the Convex union it
 * receives has its members typed as a generic array (e.g. when callers
 * write `v.union(...Object.values(SomeEnum).map(v.literal))` and the
 * `.map` widens away the tuple shape). Tests written in tuple form can
 * wrap their expected schema with this helper to match.
 *
 * ```ts
 * const ordered: z.ZodUnion<readonly [
 *   z.ZodLiteral<1>,
 *   z.ZodLiteral<2>,
 *   z.ZodLiteral<3>,
 * ]> = z.union([z.literal(1), z.literal(2), z.literal(3)]);
 *
 * const widened: z.ZodUnion<readonly z.ZodLiteral<1 | 2 | 3>[]> =
 *   ignoreZodUnionOrder(ordered);
 * ```
 */
function ignoreZodUnionOrder<
  Members extends readonly z.ZodLiteral<zCore.util.Literal>[],
>(
  union: z.ZodUnion<Members>,
): Members extends readonly z.ZodLiteral<infer V>[]
  ? // ↓ tuple of single-value literals → array of one multi-value literal
    z.ZodUnion<readonly z.ZodLiteral<V>[]>
  : never {
  return union as any;
}

describe("ignoreZodUnionOrder", () => {
  test("collapses a tuple of literals into a generic-array multi-value literal union", () => {
    const unionWithOrder = z.union([z.literal(1), z.literal(2), z.literal(3)]);
    expectTypeOf(unionWithOrder).toEqualTypeOf<
      z.ZodUnion<readonly [z.ZodLiteral<1>, z.ZodLiteral<2>, z.ZodLiteral<3>]>
    >();

    const unionWithoutOrder = ignoreZodUnionOrder(unionWithOrder);
    expectTypeOf(unionWithoutOrder).toEqualTypeOf<
      z.ZodUnion<readonly z.ZodLiteral<1 | 2 | 3>[]>
    >();
  });

  test("returns the same runtime schema (identity at runtime)", () => {
    const ordered = z.union([z.literal("a"), z.literal("b")]);
    const widened = ignoreZodUnionOrder(ordered);
    expect(widened).toBe(ordered);
    // The widened schema still parses values as before.
    expect(widened.parse("a")).toBe("a");
    expect(widened.parse("b")).toBe("b");
    expect(widened.safeParse("c").success).toBe(false);
  });

  test("widens a single-literal tuple union to an array of that literal", () => {
    const single = z.union([z.literal("only")]);
    expectTypeOf(ignoreZodUnionOrder(single)).toEqualTypeOf<
      z.ZodUnion<readonly z.ZodLiteral<"only">[]>
    >();
  });

  test("handles literals of mixed primitive types", () => {
    const mixed = z.union([z.literal("a"), z.literal(1), z.literal(true)]);
    expectTypeOf(ignoreZodUnionOrder(mixed)).toEqualTypeOf<
      z.ZodUnion<readonly z.ZodLiteral<"a" | 1 | true>[]>
    >();
  });
});
