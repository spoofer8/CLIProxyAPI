import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { Sheet } from '@/components/ui/Sheet';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/Table';
import {
  usersApi,
  type ManagedUser,
  type RequestFilters,
  type UserManagementSettings,
  type UserRequest,
  type UserRequestDetail,
} from '@/services/api/users';
import { useNotificationStore } from '@/stores/useNotificationStore';
import { getErrorMessage } from '@/utils/helpers';
import { Conversation } from './Conversation';
import { KeysDialog } from './KeysDialog';
import { parsePreview, prettyContent, quotaValue, type QuotaMode } from './logic';
import { useSessionScope } from './useSessionScope';
import styles from './UsersPage.module.scss';

const EMPTY_FILTERS: RequestFilters = { model: '', status: '', since: '' };
const omissionKeys = [
  'empty_body',
  'body_not_read',
  'inspection_limit',
  'invalid_json',
  'content_encoding',
  'preview_limit',
];

export function UserWorkspace({
  initialUser,
  settings,
  refresh,
  onUpdated,
}: {
  initialUser: ManagedUser;
  settings: UserManagementSettings | null;
  refresh: number;
  onUpdated: (user: ManagedUser) => void;
}) {
  const { t, i18n } = useTranslation();
  const { current, signal } = useSessionScope();
  const notify = useNotificationStore((state) => state.showNotification);
  const [user, setUser] = useState(initialUser);
  const [error, setError] = useState('');
  const [userLoading, setUserLoading] = useState(true);
  const [requests, setRequests] = useState<UserRequest[]>([]);
  const [before, setBefore] = useState(0);
  const [loading, setLoading] = useState(true);
  const [draftFilters, setDraftFilters] = useState<RequestFilters>(EMPTY_FILTERS);
  const [filters, setFilters] = useState<RequestFilters>(EMPTY_FILTERS);
  const [detail, setDetail] = useState<UserRequestDetail | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [raw, setRaw] = useState(false);
  const [quotaOpen, setQuotaOpen] = useState(false);
  const [quotaMode, setQuotaMode] = useState<QuotaMode>('inherit');
  const [quotaDraft, setQuotaDraft] = useState('');
  const [quotaError, setQuotaError] = useState('');
  const [saving, setSaving] = useState(false);
  const [keysOpen, setKeysOpen] = useState(false);
  const activitySequence = useRef(0);
  const userSequence = useRef(0);
  const detailSequence = useRef(0);
  const id = initialUser.id;
  const format = (value: number | null | undefined) =>
    value == null ? '—' : value.toLocaleString(i18n.language);

  useEffect(() => {
    const sequence = ++userSequence.current;
    setUserLoading(true);
    void usersApi
      .get(id, signal())
      .then((value) => {
        if (!current() || userSequence.current !== sequence) return;
        setUser(value);
        onUpdated(value);
      })
      .catch((failure) => {
        if (current() && userSequence.current === sequence)
          setError(getErrorMessage(failure, t('users.load_failed')));
      })
      .finally(() => {
        if (current() && userSequence.current === sequence) setUserLoading(false);
      });
  }, [id, refresh, current, signal, onUpdated, t]);

  const loadRequests = useCallback(
    async (cursor = 0) => {
      if (!current()) return;
      const sequence = ++activitySequence.current;
      setLoading(true);
      setError('');
      if (!cursor) {
        setRequests([]);
        setBefore(0);
        setDetailOpen(false);
        setDetail(null);
        detailSequence.current++;
      }
      try {
        const result = await usersApi.requests(id, filters, cursor, signal());
        if (!current() || activitySequence.current !== sequence) return;
        setRequests((previous) => (cursor ? [...previous, ...result.requests] : result.requests));
        setBefore(result.nextBefore);
      } catch (failure) {
        if (current() && activitySequence.current === sequence)
          setError(getErrorMessage(failure, t('users.load_failed')));
      } finally {
        if (current() && activitySequence.current === sequence) setLoading(false);
      }
    },
    [current, signal, id, filters, t]
  );
  useEffect(() => {
    void loadRequests();
  }, [loadRequests, refresh]);

  const inspect = async (requestId: string) => {
    const sequence = ++detailSequence.current;
    setDetailOpen(true);
    setDetail(null);
    setDetailError('');
    setDetailLoading(true);
    setRaw(false);
    try {
      const result = await usersApi.request(requestId, signal());
      if (current() && sequence === detailSequence.current) setDetail(result);
    } catch (failure) {
      if (current() && sequence === detailSequence.current)
        setDetailError(getErrorMessage(failure, t('users.load_failed')));
    } finally {
      if (current() && sequence === detailSequence.current) setDetailLoading(false);
    }
  };

  const openQuota = () => {
    setQuotaMode(
      user.monthlyTokenLimit == null
        ? 'inherit'
        : user.monthlyTokenLimit === 0
          ? 'unlimited'
          : 'custom'
    );
    setQuotaDraft(user.monthlyTokenLimit ? String(user.monthlyTokenLimit) : '');
    setQuotaError('');
    setQuotaOpen(true);
  };
  const saveQuota = async () => {
    if (!current() || saving) return;
    const value = quotaValue(quotaMode, quotaDraft);
    if (value === undefined) {
      setQuotaError(t('users.quota_valid', { minimum: 1 }));
      return;
    }
    setSaving(true);
    setQuotaError('');
    // A quota mutation supersedes any earlier user read still in flight.
    userSequence.current++;
    try {
      const result = await usersApi.setQuota(id, value, signal());
      if (!current()) return;
      userSequence.current++;
      setUser((previous) => ({ ...previous, ...result }));
      onUpdated(result);
      setQuotaOpen(false);
      notify(t('users.quota_saved'), 'success');
    } catch (failure) {
      if (current()) setQuotaError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (current()) {
        setSaving(false);
        setUserLoading(false);
      }
    }
  };

  const usage = user.usage;
  const limit = user.monthlyTokenLimit ?? settings?.defaultMonthlyTokens ?? 0;
  const percent = limit > 0 ? Math.min(100, ((usage?.totalTokens ?? 0) / limit) * 100) : 0;
  const monthDate = usage?.period ? new Date(usage.period + '-01T00:00:00Z') : new Date();
  const month = monthDate.toLocaleDateString(i18n.language, {
    month: 'long',
    year: 'numeric',
    timeZone: 'UTC',
  });
  const body = detail ? parsePreview(detail.bodyPreview) : null;

  return (
    <>
      <section className={styles.account} aria-busy={userLoading}>
        <div className={styles.sectionHeading}>
          <div>
            <div className={styles.accountTitle}>
              <h2>{user.displayName || user.email}</h2>
              <span className={styles.badge}>
                {t(user.status === 'active' ? 'users.active' : 'users.disabled')}
              </span>
            </div>
            <p className={styles.description}>
              {user.email}
              {user.role === 'admin' ? ' · ' + t('users.administrator') : ''}
            </p>
          </div>
          <Button variant="secondary" size="sm" onClick={() => setKeysOpen(true)}>
            {t('users.api_keys')}
          </Button>
        </div>
        <div className={styles.sectionHeading}>
          <h3>{t('users.monthly_usage')}</h3>
          <span className={styles.hint}>{t('users.month_utc', { month })}</span>
        </div>
        <div className={styles.usageGrid}>
          <div className={styles.primaryUsage}>
            <span>{t('users.tokens_used')}</span>
            <strong>{format(usage?.totalTokens)}</strong>
            <span>
              {limit > 0
                ? t('users.quota_of', { limit: format(limit) })
                : t(
                    user.monthlyTokenLimit == null && !settings
                      ? 'users.quota_default'
                      : 'users.quota_unlimited'
                  )}
            </span>
          </div>
          <div>
            <span>{t('users.input_tokens')}</span>
            <strong>{format(usage?.inputTokens)}</strong>
          </div>
          <div>
            <span>{t('users.output_tokens')}</span>
            <strong>{format(usage?.outputTokens)}</strong>
          </div>
          <div title={t('users.usage_records_hint')}>
            <span>{t('users.usage_records')}</span>
            <strong>{format(usage?.requestCount)}</strong>
          </div>
        </div>
        {limit > 0 && (
          <progress
            className={styles.quotaProgress}
            max={100}
            value={percent}
            aria-label={t('users.monthly_usage')}
          />
        )}
        <div className={styles.sectionHeading}>
          <span className={styles.hint}>
            {t(user.monthlyTokenLimit == null ? 'users.quota_default' : 'users.quota_override')}
            {settings ? ' · ' + t(settings.enforce ? 'users.enforced' : 'users.tracking_only') : ''}
          </span>
          <Button variant="secondary" size="sm" disabled={userLoading} onClick={openQuota}>
            {t('users.edit_quota')}
          </Button>
        </div>
      </section>
      <section className={styles.activity}>
        <div className={styles.activityHeader}>
          <h2>{t('users.activity')}</h2>
          {settings && (
            <p className={styles.description}>
              {settings.captureEnabled
                ? t('users.retention', { days: settings.retentionDays })
                : t('users.capture_off')}
            </p>
          )}
        </div>
        <form
          className={styles.filters}
          onSubmit={(event) => {
            event.preventDefault();
            setFilters({ ...draftFilters });
          }}
        >
          <Input
            label={t('users.model_filter')}
            placeholder={t('users.model_filter')}
            value={draftFilters.model}
            onChange={(event) =>
              setDraftFilters((previous) => ({ ...previous, model: event.target.value }))
            }
          />
          <div className={styles.selectField}>
            <label id="users-status-label">{t('users.status_filter')}</label>
            <Select
              value={draftFilters.status}
              ariaLabelledBy="users-status-label"
              options={[
                { value: '', label: t('users.all_statuses') },
                { value: 'success', label: t('users.success') },
                { value: 'error', label: t('users.errors') },
              ]}
              onChange={(status) => setDraftFilters((previous) => ({ ...previous, status }))}
            />
          </div>
          <Input
            label={t('users.since')}
            type="datetime-local"
            value={draftFilters.since}
            onChange={(event) =>
              setDraftFilters((previous) => ({ ...previous, since: event.target.value }))
            }
          />
          <div className={styles.actions}>
            <Button type="submit" variant="secondary" size="sm" disabled={loading}>
              {t('users.apply_filters')}
            </Button>
            {(filters.model || filters.status || filters.since) && (
              <Button
                variant="ghost"
                size="sm"
                type="button"
                onClick={() => {
                  setDraftFilters(EMPTY_FILTERS);
                  setFilters(EMPTY_FILTERS);
                }}
              >
                {t('users.reset_filters')}
              </Button>
            )}
          </div>
        </form>
        {error && (
          <div className="error-box" role="alert">
            {error}
          </div>
        )}
        <div aria-busy={loading}>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.time')}</TableHead>
                <TableHead>{t('users.model')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead alignRight>{t('users.tokens')}</TableHead>
                <TableHead alignRight>{t('users.duration')}</TableHead>
                <TableHead>
                  <span className={styles.srOnly}>{t('users.inspect')}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {requests.map((request) => (
                <TableRow key={request.id} selected={detail?.id === request.id}>
                  <TableCell>
                    <time dateTime={request.at}>
                      {new Date(request.at).toLocaleTimeString(i18n.language)}
                      <small className={styles.cellSecondary}>
                        {new Date(request.at).toLocaleDateString(i18n.language)}
                      </small>
                    </time>
                  </TableCell>
                  <TableCell>
                    <strong className={styles.modelName}>
                      {request.model || t('users.model_unspecified')}
                    </strong>
                    <small className={styles.cellSecondary}>
                      {request.method} {request.path}
                    </small>
                  </TableCell>
                  <TableCell>
                    <span
                      className={[
                        styles.badge,
                        request.statusCode >= 400 ? styles.errorBadge : '',
                      ].join(' ')}
                    >
                      {request.statusCode || t('users.in_progress')}
                    </span>
                  </TableCell>
                  <TableCell alignRight>{format(request.totalTokens)}</TableCell>
                  <TableCell alignRight>
                    {request.durationMs == null
                      ? '—'
                      : request.durationMs < 1000
                        ? format(request.durationMs) + ' ms'
                        : (request.durationMs / 1000).toLocaleString(i18n.language, {
                            maximumFractionDigits: 1,
                          }) + ' s'}
                  </TableCell>
                  <TableCell>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => void inspect(request.id)}
                      aria-label={t('users.inspect') + ' · ' + (request.model || request.id)}
                    >
                      {t('users.inspect')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {!requests.length && (
            <p className={styles.empty}>{loading ? t('common.loading') : t('users.no_requests')}</p>
          )}
        </div>
        <div className={styles.sectionHeading}>
          <span className={styles.hint}>
            {t('users.requests_count', { count: requests.length })}
          </span>
          {before > 0 && (
            <Button
              variant="secondary"
              size="sm"
              loading={loading}
              onClick={() => void loadRequests(before)}
            >
              {t('users.load_more_requests')}
            </Button>
          )}
        </div>
      </section>
      <Sheet
        open={detailOpen}
        size="lg"
        title={t('users.request_details')}
        description={detail ? new Date(detail.at).toLocaleString(i18n.language) : undefined}
        onClose={() => {
          detailSequence.current++;
          setDetailOpen(false);
          setDetail(null);
        }}
      >
        {detailLoading && <p className={styles.empty}>{t('common.loading')}</p>}
        {detailError && (
          <div className="error-box" role="alert">
            {detailError}
          </div>
        )}
        {detail && (
          <div className={styles.inspector}>
            <div className={styles.requestMeta}>
              <strong>{detail.model || t('users.model_unspecified')}</strong>
              <span>
                {detail.method} {detail.path}
              </span>
              {detail.provider && <span>{detail.provider}</span>}
              <span>{detail.statusCode || t('users.in_progress')}</span>
              <span>
                {format(detail.totalTokens)} {t('users.tokens')}
              </span>
            </div>
            {detail.bodyTruncated && <p className={styles.notice}>{t('users.truncated')}</p>}
            {detail.bodyOmittedReason && (
              <p className={styles.notice}>
                {t(
                  'users.' +
                    (omissionKeys.includes(detail.bodyOmittedReason)
                      ? detail.bodyOmittedReason
                      : 'body_unavailable')
                )}
              </p>
            )}
            <div className={styles.actions}>
              <Button
                variant={raw ? 'secondary' : 'primary'}
                size="sm"
                aria-pressed={!raw}
                onClick={() => setRaw(false)}
              >
                {t('users.conversation')}
              </Button>
              <Button
                variant={raw ? 'primary' : 'secondary'}
                size="sm"
                aria-pressed={raw}
                onClick={() => setRaw(true)}
              >
                {t('users.sanitized_json')}
              </Button>
            </div>
            {raw ? (
              <pre className={styles.rawJson}>
                {prettyContent(body) || t('users.body_unavailable')}
              </pre>
            ) : (
              <Conversation body={body} />
            )}
          </div>
        )}
      </Sheet>
      <Modal
        open={quotaOpen}
        title={t('users.edit_quota')}
        closeDisabled={saving}
        onClose={() => setQuotaOpen(false)}
        footer={
          <>
            <Button variant="secondary" disabled={saving} onClick={() => setQuotaOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="user-quota-form" loading={saving}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <form
          id="user-quota-form"
          onSubmit={(event) => {
            event.preventDefault();
            void saveQuota();
          }}
        >
          <p>{user.displayName || user.email}</p>
          <div className={styles.selectField}>
            <label id="users-quota-label">{t('users.quota_mode')}</label>
            <Select
              value={quotaMode}
              ariaLabelledBy="users-quota-label"
              options={[
                { value: 'inherit', label: t('users.quota_inherit') },
                { value: 'unlimited', label: t('users.quota_unlimited') },
                { value: 'custom', label: t('users.quota_custom') },
              ]}
              onChange={(value) => setQuotaMode(value as QuotaMode)}
            />
          </div>
          {quotaMode === 'custom' && (
            <Input
              label={t('users.quota_limit')}
              type="number"
              min="1"
              step="1"
              required
              value={quotaDraft}
              onChange={(event) => setQuotaDraft(event.target.value)}
            />
          )}
          <p className={styles.hint}>
            {settings?.enforce ? t('users.enforced') : t('users.tracking_hint')}
          </p>
          {quotaError && (
            <div className="error-box" role="alert">
              {quotaError}
            </div>
          )}
        </form>
      </Modal>
      {keysOpen && <KeysDialog user={user} onClose={() => setKeysOpen(false)} />}
    </>
  );
}
