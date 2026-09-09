import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import {
  financialApi,
  normalizeUsd,
  formatUsd,
  type ModelPrice,
  type PriceRates,
} from '@/services/api/financial';
import { useNotificationStore } from '@/stores/useNotificationStore';
import { getErrorMessage } from '@/utils/helpers';
import { useSessionScope } from './useSessionScope';
import styles from './Financial.module.scss';

const rateFields = ['input', 'output', 'cacheRead', 'cacheWrite', 'reasoning'] as const;
const emptyRates = { input: '', output: '', cacheRead: '', cacheWrite: '', reasoning: '' };
const safeSourceUrl = (value: string) => {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : null;
  } catch {
    return null;
  }
};
export function PricingDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const { current, signal } = useSessionScope();
  const notify = useNotificationStore((state) => state.showNotification);
  const [prices, setPrices] = useState<ModelPrice[]>([]);
  const [search, setSearch] = useState('');
  const [visible, setVisible] = useState(60);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<ModelPrice | null>(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [provider, setProvider] = useState('');
  const [model, setModel] = useState('');
  const [draft, setDraft] = useState(emptyRates);
  const [formError, setFormError] = useState('');
  const sequence = useRef(0);
  const isOpen = useRef(open);
  useEffect(() => {
    isOpen.current = open;
  }, [open]);
  const editor = useRef<HTMLDivElement>(null);
  const active = () => current() && isOpen.current;
  const load = useCallback(async () => {
    const request = ++sequence.current;
    setLoading(true);
    setError('');
    try {
      const result = await financialApi.prices(signal());
      if (current() && isOpen.current && sequence.current === request) setPrices(result);
      return result;
    } catch (failure) {
      if (current() && isOpen.current && sequence.current === request)
        setError(getErrorMessage(failure, t('users.finance.load_failed')));
      return null;
    } finally {
      if (current() && isOpen.current && sequence.current === request) setLoading(false);
    }
  }, [current, signal, t]);
  useEffect(() => {
    if (open) {
      setSelected(null);
      setEditorOpen(false);
      setSearch('');
      setVisible(60);
      void load();
    } else sequence.current++;
  }, [load, open]);
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return prices.filter((price) =>
      (price.provider + ' ' + price.model).toLowerCase().includes(needle)
    );
  }, [prices, search]);
  const select = (price: ModelPrice | null) => {
    setSelected(price);
    setProvider(price?.provider ?? '');
    setModel(price?.model ?? '');
    setDraft(
      price
        ? {
            input: price.input,
            output: price.output,
            cacheRead: price.cacheRead ?? '',
            cacheWrite: price.cacheWrite ?? '',
            reasoning: price.reasoning ?? '',
          }
        : emptyRates
    );
    setFormError('');
    setEditorOpen(true);
    window.setTimeout(() => editor.current?.scrollIntoView({ block: 'nearest' }), 0);
  };
  const save = async () => {
    if (!active() || busy) return;
    const rates: PriceRates = {
      input: '',
      output: '',
      cacheRead: null,
      cacheWrite: null,
      reasoning: null,
    };
    if (!provider.trim() || !model.trim()) {
      setFormError(t('users.finance.provider_model_required'));
      return;
    }
    for (const field of rateFields) {
      const value = draft[field].trim(),
        optional = field !== 'input' && field !== 'output';
      const normalized = value ? normalizeUsd(value, 12) : null;
      if ((!value && !optional) || (value && normalized === null)) {
        setFormError(t('users.finance.invalid_rate'));
        return;
      }
      if (field === 'input' || field === 'output') rates[field] = normalized ?? '';
      else rates[field] = normalized;
    }
    setBusy(true);
    setFormError('');
    try {
      const price = await financialApi.setPrice(provider.trim(), model.trim(), rates, signal());
      if (!active()) return;
      setSelected(price);
      notify(t('users.finance.price_saved'), 'success');
      await load();
    } catch (failure) {
      if (active()) setFormError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (active()) setBusy(false);
    }
  };
  const reset = async () => {
    if (!active() || busy || !selected) return;
    setBusy(true);
    setFormError('');
    try {
      await financialApi.resetPrice(selected.provider, selected.model, signal());
      if (!active()) return;
      notify(t('users.finance.price_restored'), 'success');
      setEditorOpen(false);
      setSelected(null);
      await load();
    } catch (failure) {
      if (active()) setFormError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (active()) setBusy(false);
    }
  };
  const refresh = async () => {
    if (!active() || busy) return;
    setBusy(true);
    setError('');
    try {
      await financialApi.refreshPrices(signal());
      if (!active()) return;
      notify(t('users.finance.prices_refreshed'), 'success');
      const result = await load();
      if (active() && selected && result)
        setSelected(
          result.find(
            (price) => price.provider === selected.provider && price.model === selected.model
          ) ?? selected
        );
    } catch (failure) {
      if (active()) setError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (active()) setBusy(false);
    }
  };
  const source = selected ? safeSourceUrl(selected.sourceUrl) : null;
  return (
    <Modal
      open={open}
      title={t('users.finance.pricing_title')}
      onClose={onClose}
      closeDisabled={busy}
      width={820}
      footer={
        <Button variant="secondary" disabled={busy} onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <p className={styles.hint}>{t('users.finance.price_help')}</p>
      <div className={styles.heading}>
        <Input
          aria-label={t('users.finance.price_search')}
          placeholder={t('users.finance.price_search')}
          value={search}
          onChange={(event) => {
            setSearch(event.target.value);
            setVisible(60);
          }}
        />
        <div className={styles.actions}>
          <Button
            size="sm"
            variant="secondary"
            loading={busy}
            disabled={loading}
            onClick={() => void refresh()}
          >
            {t('users.finance.refresh_prices')}
          </Button>
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => select(null)}>
            {t('users.finance.add_price')}
          </Button>
        </div>
      </div>
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      {loading && <p className={styles.hint}>{t('common.loading')}</p>}
      <div className={styles.catalog} aria-busy={loading}>
        {filtered.slice(0, visible).map((price) => (
          <button
            type="button"
            key={price.id}
            className={styles.priceRow}
            disabled={busy}
            onClick={() => select(price)}
          >
            <span className={styles.priceModel}>
              <strong>{price.model}</strong>
              <div className={styles.hint}>
                {price.provider} ·{' '}
                {t(price.manual ? 'users.finance.manual_price' : 'users.finance.published_price')}
              </div>
            </span>
            <span className={styles.priceNumber}>
              {t('users.finance.rate_input')}: {formatUsd(price.input)}
              <br />
              {t('users.finance.rate_output')}: {formatUsd(price.output)}
            </span>
          </button>
        ))}
        {!filtered.length && !loading && (
          <p className={styles.notice}>{t('users.finance.no_prices')}</p>
        )}
      </div>
      {filtered.length > visible && (
        <Button
          size="sm"
          variant="secondary"
          onClick={() => setVisible((previous) => previous + 60)}
        >
          {t('users.finance.more_prices', { count: filtered.length - visible })}
        </Button>
      )}
      <p className={styles.hint}>{t('users.finance.catalog_source')}</p>
      {editorOpen && (
        <div ref={editor} className={styles.editor}>
          <div className={styles.heading}>
            <h3>
              {t(selected?.manual ? 'users.finance.edit_override' : 'users.finance.add_price')}
            </h3>
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => setEditorOpen(false)}
            >
              {t('common.cancel')}
            </Button>
          </div>
          <div className={styles.formGrid}>
            <Input
              label={t('users.finance.provider')}
              value={provider}
              maxLength={128}
              disabled={busy || Boolean(selected)}
              onChange={(event) => setProvider(event.target.value)}
            />
            <Input
              label={t('users.finance.model')}
              value={model}
              maxLength={256}
              disabled={busy || Boolean(selected)}
              onChange={(event) => setModel(event.target.value)}
            />
          </div>
          {selected && (
            <>
              <p className={styles.hint}>
                {selected.source}
                {source && (
                  <>
                    {' '}
                    ·{' '}
                    <a href={source} target="_blank" rel="noopener noreferrer">
                      {t('users.finance.provider_pricing')}
                    </a>
                  </>
                )}{' '}
                ·{' '}
                {t('users.finance.updated', {
                  time: new Date(selected.updatedAt).toLocaleString(i18n.language),
                })}
              </p>
              {selected.contextTiers.map((tier) => (
                <div className={styles.tier} key={tier.aboveTokens}>
                  <strong>
                    {t('users.finance.context_tier', {
                      tokens: tier.aboveTokens.toLocaleString(i18n.language),
                    })}
                  </strong>
                  <p className={styles.hint}>
                    {rateFields
                      .map(
                        (field) =>
                          t('users.finance.rate_' + field) +
                          ': ' +
                          (tier[field] == null
                            ? t('users.finance.unknown')
                            : formatUsd(tier[field]))
                      )
                      .join(' · ')}
                  </p>
                </div>
              ))}
            </>
          )}
          <p className={styles.hint}>{t('users.finance.rate_units')}</p>
          <div className={styles.formGrid}>
            {rateFields.map((field) => (
              <Input
                key={field}
                label={t('users.finance.rate_' + field)}
                inputMode="decimal"
                value={draft[field]}
                disabled={busy}
                placeholder={
                  field === 'input' || field === 'output'
                    ? t('users.finance.required')
                    : t('users.finance.unknown')
                }
                onChange={(event) =>
                  setDraft((previous) => ({ ...previous, [field]: event.target.value }))
                }
              />
            ))}
          </div>
          <p className={styles.hint}>{t('users.finance.optional_rates')}</p>
          {Boolean(selected?.contextTiers.length) && (
            <p className={styles.notice}>{t('users.finance.override_flat')}</p>
          )}
          {formError && (
            <div className="error-box" role="alert">
              {formError}
            </div>
          )}
          <div className={styles.actions}>
            <Button loading={busy} onClick={() => void save()}>
              {t('users.finance.save_price')}
            </Button>
            {selected?.manual && (
              <Button variant="secondary" disabled={busy} onClick={() => void reset()}>
                {t('users.finance.restore_price')}
              </Button>
            )}
          </div>
        </div>
      )}
    </Modal>
  );
}
