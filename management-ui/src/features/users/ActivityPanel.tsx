import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { activityApi, type ActivityFilters, type ActivityRequest } from '@/services/api/activity';
import { getErrorMessage } from '@/utils/helpers';
import { useSessionScope } from './useSessionScope';
import { RequestInspector } from './RequestInspector';
import styles from './Activity.module.scss';

const EMPTY: ActivityFilters = { query: '', model: '', status: '', since: '', sessionId: '' };
interface Props {
  userId: string;
  refreshKey?: number | string;
  onChanged?: () => void;
  sessionId?: string;
  onSessionChange?: (sessionId: string) => void;
}

export function ActivityPanel(props: Props) {
  return <ActivityPanelContent key={props.userId + ':' + (props.sessionId ?? '')} {...props} />;
}

function ActivityPanelContent({
  userId,
  refreshKey,
  sessionId: initialSessionId = '',
  onSessionChange,
}: Props) {
  const { t, i18n } = useTranslation();
  const { current, signal } = useSessionScope();
  const statusLabel = useId();
  const [draft, setDraft] = useState<ActivityFilters>({ ...EMPTY, sessionId: initialSessionId });
  const [filters, setFilters] = useState<ActivityFilters>({
    ...EMPTY,
    sessionId: initialSessionId,
  });
  const [requests, setRequests] = useState<ActivityRequest[]>([]);
  const [next, setNext] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState('');
  const sequence = useRef(0);
  const load = useCallback(
    async (before = 0) => {
      if (!current()) return;
      const generation = ++sequence.current;
      setLoading(true);
      setError('');
      if (!before) {
        setRequests([]);
        setNext(0);
      }
      try {
        const result = await activityApi.list(userId, filters, before, signal());
        if (!current() || generation !== sequence.current) return;
        setRequests((previous) =>
          before
            ? [
                ...previous,
                ...result.requests.filter((item) => !previous.some((old) => old.id === item.id)),
              ]
            : result.requests
        );
        setNext(result.nextBefore);
      } catch (failure) {
        if (current() && generation === sequence.current)
          setError(getErrorMessage(failure, t('users.load_failed')));
      } finally {
        if (current() && generation === sequence.current) setLoading(false);
      }
    },
    [current, signal, userId, filters, t]
  );
  useEffect(() => {
    void load();
  }, [load, refreshKey]);
  const session = (sessionId: string) => {
    onSessionChange?.(sessionId);
    setSelected('');
    setDraft((value) => ({ ...value, sessionId }));
    setFilters((value) => ({ ...value, sessionId }));
  };
  const format = (value: number | null) =>
    value == null ? '—' : value.toLocaleString(i18n.language);
  return (
    <section className={styles.panel}>
      <div className={styles.heading}>
        <div>
          <h2>{t('users.activity')}</h2>
          <p>{t('users.activity_retained')}</p>
        </div>
        <Button variant="secondary" size="sm" loading={loading} onClick={() => void load()}>
          {t('users.activity_refresh')}
        </Button>
      </div>
      <form
        className={styles.filters}
        onSubmit={(event) => {
          event.preventDefault();
          setFilters({ ...draft });
        }}
      >
        <Input
          label={t('users.activity_search')}
          placeholder={t('users.activity_search_placeholder')}
          value={draft.query}
          onChange={(event) => setDraft((value) => ({ ...value, query: event.target.value }))}
        />
        <Input
          label={t('users.model_filter')}
          value={draft.model}
          onChange={(event) => setDraft((value) => ({ ...value, model: event.target.value }))}
        />
        <div className={styles.select}>
          <label id={statusLabel}>{t('users.status_filter')}</label>
          <Select
            ariaLabelledBy={statusLabel}
            value={draft.status}
            options={[
              { value: '', label: t('users.all_statuses') },
              { value: 'success', label: t('users.success') },
              { value: 'error', label: t('users.errors') },
            ]}
            onChange={(status) => setDraft((value) => ({ ...value, status }))}
          />
        </div>
        <Input
          label={t('users.since')}
          type="datetime-local"
          value={draft.since}
          onChange={(event) => setDraft((value) => ({ ...value, since: event.target.value }))}
        />
        <div className={styles.actions}>
          <Button type="submit" variant="secondary" size="sm" disabled={loading}>
            {t('users.apply_filters')}
          </Button>
          {Object.values(filters).some(Boolean) && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                onSessionChange?.('');
                setDraft(EMPTY);
                setFilters(EMPTY);
              }}
            >
              {t('users.reset_filters')}
            </Button>
          )}
        </div>
      </form>
      {filters.sessionId && (
        <div className={styles.sessionFilter}>
          <span>
            {t('users.activity_session')}: <code>{filters.sessionId}</code>
          </span>
          <Button variant="ghost" size="sm" onClick={() => session('')}>
            {t('users.activity_all_sessions')}
          </Button>
        </div>
      )}
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      <div className={styles.list} aria-busy={loading}>
        {requests.map((request) => (
          <article className={styles.request} key={request.id}>
            <div className={styles.requestLine}>
              <strong>{request.model || t('users.model_unspecified')}</strong>
              <span className={request.statusCode >= 400 ? styles.errorStatus : styles.status}>
                {request.statusCode || t('users.in_progress')}
              </span>
              <time dateTime={request.at}>
                {new Date(request.at).toLocaleString(i18n.language)}
              </time>
            </div>
            <button
              className={styles.prompt}
              onClick={() => setSelected(request.id)}
              aria-label={t('users.inspect') + ': ' + (request.model || request.id)}
            >
              {request.promptPreview || t('users.activity_no_preview')}
            </button>
            <div className={styles.requestFooter}>
              <span>
                {request.method} {request.path}
              </span>
              <span>
                {format(request.totalTokens)} {t('users.tokens')}
              </span>
              {request.durationMs != null && (
                <span>
                  {(request.durationMs / 1000).toLocaleString(i18n.language, {
                    maximumFractionDigits: 1,
                  })}{' '}
                  s
                </span>
              )}
              <div className={styles.actions}>
                {request.sessionId && (
                  <Button variant="ghost" size="sm" onClick={() => session(request.sessionId)}>
                    {t('users.activity_view_session')}
                  </Button>
                )}
                <Button variant="secondary" size="sm" onClick={() => setSelected(request.id)}>
                  {t('users.inspect')}
                </Button>
              </div>
            </div>
          </article>
        ))}
        {!requests.length && (
          <p className={styles.empty}>{loading ? t('common.loading') : t('users.no_requests')}</p>
        )}
      </div>
      <div className={styles.heading}>
        <span className={styles.hint}>{t('users.requests_count', { count: requests.length })}</span>
        {next > 0 && (
          <Button variant="secondary" size="sm" loading={loading} onClick={() => void load(next)}>
            {t('users.load_more_requests')}
          </Button>
        )}
      </div>
      {selected && (
        <RequestInspector
          key={selected}
          requestId={selected}
          onClose={() => setSelected('')}
          onSession={session}
        />
      )}
    </section>
  );
}
