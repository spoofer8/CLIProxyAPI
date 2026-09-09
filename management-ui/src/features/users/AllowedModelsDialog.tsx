import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { useApiKeysForModels } from '@/hooks/useApiKeysForModels';
import { modelsApi } from '@/services/api/models';
import { userPermissionsApi, type UserPermission } from '@/services/api/userPermissions';
import { useAuthStore } from '@/stores/useAuthStore';
import { useNotificationStore } from '@/stores/useNotificationStore';
import { getErrorMessage } from '@/utils/helpers';
import { isModelAllow, isPattern, replaceModelAllows } from './modelPermissions';
import { useSessionScope } from './useSessionScope';
import styles from './AllowedModelsDialog.module.scss';

export function AllowedModelsDialog({ userId, onClose }: { userId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const { current, signal } = useSessionScope();
  const apiBase = useAuthStore((state) => state.apiBase);
  const resolveKeys = useApiKeysForModels();
  const notify = useNotificationStore((state) => state.showNotification);
  const [rules, setRules] = useState<UserPermission[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [patterns, setPatterns] = useState<string[]>([]);
  const [mode, setMode] = useState<'all' | 'selected'>('all');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    setReady(false);
    setLoading(true);
    setError('');
    const loadModels = async () => {
      const keys = await resolveKeys({ force: true });
      if (!active || !current()) return [];
      return modelsApi.fetchModels(apiBase, keys[0], {}, { caseSensitive: true, signal: signal() });
    };
    void Promise.all([userPermissionsApi.get(userId, signal()), loadModels()])
      .then(([permissions, catalog]) => {
        if (!active || !current()) return;
        const allows = permissions.filter(isModelAllow).map((rule) => rule.value);
        setRules(permissions);
        setMode(allows.length ? 'selected' : 'all');
        setSelected(allows.filter((value) => !isPattern(value)));
        setPatterns(allows.filter(isPattern));
        setModels([
          ...new Set(catalog.map((model) => model.name).filter((name) => name && !isPattern(name))),
        ]);
        setReady(true);
      })
      .catch((failure) => {
        if (active && current()) setError(getErrorMessage(failure, t('users.load_failed')));
      })
      .finally(() => {
        if (active && current()) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [userId, apiBase, resolveKeys, current, signal, t]);
  const options = useMemo(
    () =>
      [
        ...new Set([
          ...models,
          ...rules
            .filter(isModelAllow)
            .map((rule) => rule.value)
            .filter((value) => !isPattern(value)),
        ]),
      ].sort(),
    [models, rules]
  );
  const visible = options.filter((model) =>
    model.toLowerCase().includes(search.trim().toLowerCase())
  );
  const save = async () => {
    if (!current() || !ready || busy) return;
    let permissions: UserPermission[];
    try {
      permissions = replaceModelAllows(rules, mode, selected, patterns);
    } catch (failure) {
      setError(
        t(
          failure instanceof Error && failure.message === 'too_many_rules'
            ? 'users.models_too_many'
            : 'users.models_one_required'
        )
      );
      return;
    }
    setBusy(true);
    setError('');
    try {
      await userPermissionsApi.replace(userId, permissions, signal());
      if (!current()) return;
      notify(t('users.models_saved'), 'success');
      onClose();
    } catch (failure) {
      if (current()) setError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (current()) setBusy(false);
    }
  };
  return (
    <Modal
      open
      title={t('users.allowed_models')}
      onClose={onClose}
      closeDisabled={busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => void save()}
            disabled={!ready || (mode === 'selected' && !selected.length && !patterns.length)}
            loading={busy}
          >
            {t('common.save')}
          </Button>
        </>
      }
    >
      <div className={styles.content}>
        <p className={styles.hint}>{t('users.models_hint')}</p>
        {loading && <p>{t('common.loading')}</p>}
        {error && (
          <div className="error-box" role="alert">
            {error}
          </div>
        )}
        {ready && (
          <>
            <fieldset className={styles.modes} disabled={busy}>
              <legend className={styles.srOnly}>{t('users.allowed_models')}</legend>
              <label>
                <input
                  type="radio"
                  name="model-access"
                  checked={mode === 'all'}
                  onChange={() => setMode('all')}
                />
                {t('users.models_all')}
              </label>
              <label>
                <input
                  type="radio"
                  name="model-access"
                  checked={mode === 'selected'}
                  onChange={() => setMode('selected')}
                />
                {t('users.models_selected')}
              </label>
            </fieldset>
            {mode === 'selected' && (
              <>
                <Input
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                  placeholder={t('users.models_search')}
                  aria-label={t('users.models_search')}
                />
                {options.some((model) => !models.includes(model)) && (
                  <p className={styles.hint}>{t('users.models_unlisted')}</p>
                )}
                <div className={styles.models}>
                  {visible.map((model) => (
                    <SelectionCheckbox
                      key={model}
                      checked={selected.includes(model)}
                      label={model}
                      ariaLabel={model}
                      disabled={
                        busy ||
                        rules.some(
                          (rule) =>
                            rule.scope === 'model' && rule.effect === 'deny' && rule.value === model
                        )
                      }
                      onChange={(checked) =>
                        setSelected((previous) =>
                          checked
                            ? [...previous, model]
                            : previous.filter((value) => value !== model)
                        )
                      }
                    />
                  ))}
                  {!visible.length && <p className={styles.hint}>{t('users.models_empty')}</p>}
                </div>
                {!selected.length && !patterns.length && (
                  <p className={styles.hint}>{t('users.models_one_required')}</p>
                )}
                {patterns.length > 0 && (
                  <div className={styles.patterns}>
                    <p className={styles.hint}>{t('users.models_patterns')}</p>
                    {patterns.map((pattern) => (
                      <div key={pattern} className={styles.pattern}>
                        <code>{pattern}</code>
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={busy}
                          aria-label={t('users.models_remove_pattern', { pattern })}
                          onClick={() =>
                            setPatterns((previous) => previous.filter((value) => value !== pattern))
                          }
                        >
                          {t('common.delete')}
                        </Button>
                      </div>
                    ))}
                  </div>
                )}
              </>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}
