/** A sandbox size: vCPU, memory and writable disk. */
export type SandboxSize = "small" | "medium" | "large" | "xl";

/** The sizes, smallest first. */
export const SIZES: readonly SandboxSize[] = ["small", "medium", "large", "xl"];

/**
 * The metadata key the size rides in: the E2B SDK cannot send the create
 * body's `size` (an Anona extension), so the gateway also reads it here, and
 * consumes it rather than storing it as metadata.
 */
export const SIZE_METADATA_KEY = "anona.size";

const ALIASES: Record<string, SandboxSize> = { s: "small", m: "medium", l: "large", xlarge: "xl", "x-large": "xl" };

/** Normalise a size name, or throw for an unknown one. */
export function parseSize(size: string): SandboxSize {
  const n = size.trim().toLowerCase();
  const s = (ALIASES[n] ?? n) as SandboxSize;
  if (!SIZES.includes(s)) throw new Error(`unknown sandbox size "${size}": use one of ${SIZES.join(", ")}`);
  return s;
}

/**
 * Sandbox create options with a size set:
 * `Sandbox.create("desktop", withSize("large", { timeoutMs: 600_000 }))`.
 * Without a size a sandbox runs at its template's own size; a size below the
 * template's minimum, or not in your plan, is refused with the sizes that
 * would work.
 */
export function withSize<T extends object = {}>(
  size: SandboxSize | string,
  opts?: T,
): T & { metadata: Record<string, string> } {
  // T is any create-options object (timeoutMs, envs, ...); a constraint of
  // { metadata?: ... } alone made `withSize("large", { timeoutMs })` fail to compile.
  const base = (opts ?? {}) as T & { metadata?: Record<string, string> };
  return { ...base, metadata: { ...(base.metadata ?? {}), [SIZE_METADATA_KEY]: parseSize(size) } };
}
