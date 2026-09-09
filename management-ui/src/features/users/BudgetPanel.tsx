import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import {
  financialApi,
  budgetPeriods,
  normalizeUsd,
  formatUsd,
  type BudgetLimits,
  type BudgetStatus,
  type CostEvent,
} from '@/services/api/financial';
import { useNotificationStore } from '@/stores/useNotificationStore';
import { getErrorMessage } from '@/utils/helpers';
import { useSessionScope } from './useSessionScope';
import styles from './Financial.module.scss';

const emptyDraft = { lifetime: '', daily: '', weekly: '', monthly: '' };
export function BudgetPanel({
  userId,
  adminExempt = false,
  refreshKey = 0,
  onChanged,
  onInspectRequest,
}: {
  userId: string;
  adminExempt?: boolean;
  refreshKey?: number;
  onChanged?: () => void;
  onInspectRequest?: (requestId: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const { current, signal } = useSessionScope();
  const notify = useNotificationStore((state) => state.showNotification);
  const [status, setStatus] = useState<BudgetStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editOpen, setEditOpen] = useState(false);
  const [draft, setDraft] = useState(emptyDraft);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState('');
  const [resolveId, setResolveId] = useState('');
  const [resolutionCost, setResolutionCost] = useState('');
  const [resolutionNote, setResolutionNote] = useState('');
  const [events, setEvents] = useState<CostEvent[]>([]);
  const [eventsTotal, setEventsTotal] = useState(0);
  const [eventsOpen, setEventsOpen] = useState(false);
  const [eventsLoading, setEventsLoading] = useState(false);
  const sequence = useRef(0);
  const eventSequence = useRef(0);
  const activeUser = useRef(userId);
  useEffect(() => {
    activeUser.current = userId;
  }, [userId]);
  const exempt = adminExempt || status?.adminExempt;
  const date = (value: string) => {
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime())
      ? '—'
      : parsed.toLocaleString(i18n.language, { timeZone: 'Europe/London', timeZoneName: 'short' });
  };
  const load = useCallback(async () => {
    const request = ++sequence.current;
    setLoading(true);
    setError('');
    try {
      const value = await financialApi.budget(userId, signal());
      if (current() && request === sequence.current && activeUser.current === userId)
        setStatus(value);
    } catch (failure) {
      if (current() && request === sequence.current)
        setError(getErrorMessage(failure, t('users.finance.load_failed')));
    } finally {
      if (current() && request === sequence.current) setLoading(false);
    }
  }, [current, signal, t, userId]);
  useEffect(() => {
    setStatus(null);
    setEditOpen(false);
    setResolveId('');
    setEvents([]);
    setEventsOpen(false);
    eventSequence.current++;
    void load();
  }, [load, refreshKey]);
  const loadEvents = async (offset = 0) => {
    const request = ++eventSequence.current;
    setEventsLoading(true);
    setEventsOpen(true);
    try {
      const result = await financialApi.events(userId, offset, signal());
      if (!current() || request !== eventSequence.current || activeUser.current !== userId) return;
      setEvents((previous) => (offset ? [...previous, ...result.events] : result.events));
      setEventsTotal(result.total);
    } catch (failure) {
      if (current() && request === eventSequence.current)
        setError(getErrorMessage(failure, t('users.finance.load_failed')));
    } finally {
      if (current() && request === eventSequence.current) setEventsLoading(false);
    }
  };
  const openEdit = () => {
    if (!status) return;
    setDraft(
      Object.fromEntries(
        budgetPeriods.map((period) => [period, status.budget[period] ?? ''])
      ) as typeof emptyDraft
    );
    setFormError('');
    setEditOpen(true);
  };
  const save = async () => {
    if (!current() || saving) return;
    const limits = {} as BudgetLimits;
    for (const period of budgetPeriods) {
      const value = draft[period].trim();
      limits[period] = value ? normalizeUsd(value) : null;
      if (value && limits[period] === null) {
        setFormError(t('users.finance.invalid_money'));
        return;
      }
    }
    setSaving(true);
    setFormError('');
    sequence.current++;
    try {
      const value = await financialApi.setBudget(userId, limits, signal());
      if (!current() || activeUser.current !== userId) return;
      setStatus(value);
      setEditOpen(false);
      notify(t('users.finance.budget_saved'), 'success');
      onChanged?.();
    } catch (failure) {
      if (current() && activeUser.current === userId)
        setFormError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (current() && activeUser.current === userId) {
        setSaving(false);
        setLoading(false);
      }
    }
  };
  const openResolution = (id: string) => {
    setResolveId(id);
    setResolutionCost('');
    setResolutionNote('');
    setFormError('');
  };
  const resolve = async () => {
    if (!current() || saving) return;
    const cost = normalizeUsd(resolutionCost.trim());
    if (cost === null || !resolutionNote.trim()) {
      setFormError(t('users.finance.resolution_required'));
      return;
    }
    setSaving(true);
    setFormError('');
    try {
      await financialApi.resolve(resolveId, cost, resolutionNote, signal());
      if (!current() || activeUser.current !== userId) return;
      setResolveId('');
      notify(t('users.finance.resolution_saved'), 'success');
      await load();
      onChanged?.();
      if (eventsOpen) void loadEvents();
    } catch (failure) {
      if (current() && activeUser.current === userId)
        setFormError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (current() && activeUser.current === userId) setSaving(false);
    }
  };
  return (
    <section className={styles.panel} aria-busy={loading}>
      <div className={styles.heading}>
        <div>
          <h3>{t('users.finance.title')}</h3>
          <p className={styles.hint}>{t('users.finance.currency')}</p>
        </div>
        <div className={styles.actions}>
          <Button variant="secondary" size="sm" loading={loading} onClick={() => void load()}>
            {t('common.refresh')}
          </Button>
          {!exempt && (
            <Button variant="secondary" size="sm" disabled={!status || loading} onClick={openEdit}>
              {t('users.finance.edit')}
            </Button>
          )}
        </div>
      </div>
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      {exempt && <p className={styles.notice}>{t('users.finance.admin_exempt')}</p>}
      {!status && !error && <p className={styles.hint}>{t('common.loading')}</p>}
      {status && (
        <>
          <div className={styles.grid}>
            {budgetPeriods.map((period) => (
              <div className={styles.metric} key={period}>
                <div className={styles.metricLabel}>{t('users.finance.period_' + period)}</div>
                <div className={styles.amount}>{formatUsd(status.spend[period])}</div>
                <p className={styles.hint}>
                  {status.budget[period] == null || exempt
                    ? t('users.finance.unlimited')
                    : t('users.finance.of_limit', { limit: formatUsd(status.budget[period]) })}
                </p>
              </div>
            ))}
          </div>
          {status.blocked && (
            <div className={styles.notice + ' ' + styles.blocked} role="status">
              <strong>{t('users.finance.blocked')}</strong>
              <p className={styles.hint}>
                {status.unresolvedRequests > 0
                  ? t('users.finance.unresolved_help')
                  : status.requiresAdminAction
                    ? t('users.finance.raise_limit')
                    : t('users.finance.available_again', { time: date(status.availableAt ?? '') })}
              </p>
            </div>
          )}
          <p className={styles.hint}>
            {t('users.finance.daily_reset', { time: date(status.dailyResetsAt) })}
          </p>
          <p className={styles.hint}>
            {t('users.finance.activation', { time: date(status.activatedAt) })}
          </p>
          {status.unpricedRequests > 0 && (
            <p className={styles.notice}>
              {t('users.finance.unpriced_count', { count: status.unpricedRequests })}
            </p>
          )}
          {status.unresolvedRequestIds.length > 0 && (
            <div className={styles.notice}>
              <strong>{t('users.finance.unresolved_title')}</strong>
              <div className={styles.requestList}>
                {status.unresolvedRequestIds.map((id) => (
                  <div key={id} className={styles.requestRow}>
                    <code>{id}</code>
                    <div className={styles.actions}>
                      {onInspectRequest && (
                        <Button size="sm" variant="secondary" onClick={() => onInspectRequest(id)}>
                          {t('users.finance.inspect')}
                        </Button>
                      )}
                      <Button size="sm" variant="secondary" onClick={() => openResolution(id)}>
                        {t('users.finance.resolve')}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
          <div className={styles.events}>
            <Button
              variant="secondary"
              size="sm"
              loading={eventsLoading}
              onClick={() => (eventsOpen ? setEventsOpen(false) : void loadEvents())}
            >
              {t(eventsOpen ? 'users.finance.hide_events' : 'users.finance.events')}
            </Button>
            {eventsOpen && (
              <div>
                {events.map((event) => (
                  <details className={styles.event} key={event.id}>
                    <summary>
                      <span>
                        {event.model || event.requestId}
                        <small className={styles.hint}> · {date(event.at)}</small>
                      </span>
                      <strong>
                        {event.costUsd === null
                          ? t('users.finance.unknown')
                          : formatUsd(event.costUsd)}
                      </strong>
                    </summary>
                    <p className={styles.hint}>
                      {event.provider} · {event.requestId}
                      {event.alias && event.alias !== event.model ? ' · ' + event.alias : ''}
                    </p>
                    {event.reason && (
                      <p className={styles.hint}>
                        {t('users.finance.reason')}:{' '}
                        {t('users.finance.reason_' + event.reason, {
                          defaultValue: event.reason.replace(/_/g, ' '),
                        })}
                      </p>
                    )}
                    <p className={styles.hint}>{t('users.finance.billing_detail')}</p>
                    <pre className={styles.pre}>
                      {JSON.stringify(
                        { tokens: event.tokenBreakdown, price: event.priceSnapshot },
                        null,
                        2
                      )}
                    </pre>
                    {event.status === 'unresolved' && (
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => openResolution(event.requestId)}
                      >
                        {t('users.finance.resolve')}
                      </Button>
                    )}
                  </details>
                ))}
                {!events.length && !eventsLoading && (
                  <p className={styles.hint}>{t('users.finance.no_events')}</p>
                )}
                {events.length < eventsTotal && (
                  <Button
                    variant="secondary"
                    size="sm"
                    loading={eventsLoading}
                    onClick={() => void loadEvents(events.length)}
                  >
                    {t('users.finance.more_events')}
                  </Button>
                )}
              </div>
            )}
          </div>
        </>
      )}
      <Modal
        open={editOpen}
        title={t('users.finance.edit')}
        onClose={() => setEditOpen(false)}
        closeDisabled={saving}
        footer={
          <>
            <Button variant="secondary" disabled={saving} onClick={() => setEditOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void save()}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <p className={styles.hint}>{t('users.finance.budget_help')}</p>
        <div className={styles.formGrid}>
          {budgetPeriods.map((period) => (
            <Input
              key={period}
              label={t('users.finance.period_' + period) + ' (USD)'}
              inputMode="decimal"
              value={draft[period]}
              placeholder={t('users.finance.unlimited')}
              disabled={saving}
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, [period]: event.target.value }))
              }
            />
          ))}
        </div>
        <p className={styles.hint}>{t('users.finance.accepted_finish')}</p>
        {formError && (
          <div className="error-box" role="alert">
            {formError}
          </div>
        )}
      </Modal>
      <Modal
        open={Boolean(resolveId)}
        title={t('users.finance.resolve')}
        onClose={() => setResolveId('')}
        closeDisabled={saving}
        footer={
          <>
            <Button variant="secondary" disabled={saving} onClick={() => setResolveId('')}>
              {t('common.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void resolve()}>
              {t('users.finance.resolve')}
            </Button>
          </>
        }
      >
        <p className={styles.hint}>{resolveId}</p>
        <p>{t('users.finance.resolve_help')}</p>
        <Input
          label={t('users.finance.total_cost')}
          inputMode="decimal"
          value={resolutionCost}
          onChange={(event) => setResolutionCost(event.target.value)}
          disabled={saving}
        />
        <Input
          label={t('users.finance.resolution_note')}
          value={resolutionNote}
          maxLength={2048}
          onChange={(event) => setResolutionNote(event.target.value)}
          disabled={saving}
        />
        {formError && (
          <div className="error-box" role="alert">
            {formError}
          </div>
        )}
      </Modal>
    </section>
  );
}
