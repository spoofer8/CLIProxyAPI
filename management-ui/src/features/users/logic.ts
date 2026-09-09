export type QuotaMode = 'inherit' | 'unlimited' | 'custom';

export function parseTokenLimit(raw: string, minimum: number): number | null {
  if (!raw.trim()) return null;
  const value = Number(raw);
  return Number.isSafeInteger(value) && value >= minimum ? value : null;
}

export function quotaValue(mode: QuotaMode, raw: string): number | null | undefined {
  if (mode === 'inherit') return null;
  if (mode === 'unlimited') return 0;
  return parseTokenLimit(raw, 1) ?? undefined;
}

export function parsePreview(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

export const isContentRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

export function prettyContent(value: unknown): string {
  if (typeof value === 'string') {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  return JSON.stringify(value, null, 2) ?? '';
}
