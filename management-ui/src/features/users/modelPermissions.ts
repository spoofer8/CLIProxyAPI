import type { UserPermission } from '@/services/api/userPermissions';

export const isModelAllow = (rule: UserPermission) =>
  rule.scope === 'model' && rule.effect === 'allow';
export const isPattern = (value: string) => /[*?]/.test(value);

/** Model identifiers are case-sensitive. Preserve all other policy dimensions. */
export function replaceModelAllows(
  rules: UserPermission[],
  mode: 'all' | 'selected',
  selected: string[],
  patterns: string[] = []
): UserPermission[] {
  const preserved = rules.filter((rule) => !isModelAllow(rule));
  if (mode === 'all') return preserved;
  const existingPatterns = new Set(
    rules
      .filter(isModelAllow)
      .map((rule) => rule.value)
      .filter(isPattern)
  );
  const values = [
    ...new Set([
      ...selected.filter((value) => value.trim() && !isPattern(value)),
      ...patterns.filter((value) => existingPatterns.has(value)),
    ]),
  ];
  if (!values.length) throw new Error('empty_selection');
  if (
    values.some((value) => preserved.some((rule) => rule.scope === 'model' && rule.value === value))
  )
    throw new Error('denied_model');
  if (preserved.length + values.length > 128) throw new Error('too_many_rules');
  return [
    ...preserved,
    ...values.map((value): UserPermission => ({ scope: 'model', value, effect: 'allow' })),
  ];
}
