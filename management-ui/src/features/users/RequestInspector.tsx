import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Sheet } from '@/components/ui/Sheet';
import {
  activityApi,
  appendActivityChunks,
  parseActivityContent,
  type ActivityChunk,
  type ActivityDirection,
  type ActivityRequest,
} from '@/services/api/activity';
import { getErrorMessage } from '@/utils/helpers';
import { Conversation } from './Conversation';
import { parsePreview } from './logic';
import { useSessionScope } from './useSessionScope';
import styles from './Activity.module.scss';

export function RequestInspector({
  requestId,
  onClose,
  onSession,
}: {
  requestId: string;
  onClose: () => void;
  onSession: (id: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const { current, signal } = useSessionScope();
  const [detail, setDetail] = useState<ActivityRequest | null>(null);
  const [error, setError] = useState('');
  const [direction, setDirection] = useState<ActivityDirection>('request');
  useEffect(() => {
    let active = true;
    void activityApi
      .detail(requestId, signal())
      .then((value) => {
        if (active && current()) setDetail(value);
      })
      .catch((failure) => {
        if (active && current()) setError(getErrorMessage(failure, t('users.load_failed')));
      });
    return () => {
      active = false;
    };
  }, [requestId, current, signal, t]);
  return (
    <Sheet
      open
      size="xl"
      title={t('users.request_details')}
      description={detail ? new Date(detail.at).toLocaleString(i18n.language) : undefined}
      onClose={onClose}
    >
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      {!detail && !error && <p className={styles.empty}>{t('common.loading')}</p>}
      {detail && (
        <div className={styles.inspector}>
          <div className={styles.metadata}>
            <strong>{detail.model || t('users.model_unspecified')}</strong>
            <span>
              {detail.method} {detail.path}
            </span>
            <span>{detail.provider}</span>
            <span>{detail.statusCode || t('users.in_progress')}</span>
            <span>
              {detail.totalTokens?.toLocaleString(i18n.language) ?? '—'} {t('users.tokens')}
            </span>
          </div>
          {detail.sessionId && (
            <div className={styles.sessionFilter}>
              <span>
                {t('users.activity_session')}: <code>{detail.sessionId}</code>
              </span>
              <Button variant="ghost" size="sm" onClick={() => onSession(detail.sessionId)}>
                {t('users.activity_view_session')}
              </Button>
            </div>
          )}
          {detail.captureState === 'interrupted' && (
            <p className={styles.notice}>{t('users.activity_interrupted')}</p>
          )}
          {detail.captureState === 'legacy_preview' && (
            <p className={styles.notice}>{t('users.activity_legacy_preview')}</p>
          )}
          <div className={styles.tabs} aria-label={t('users.activity_direction')}>
            <Button
              variant={direction === 'request' ? 'primary' : 'secondary'}
              size="sm"
              aria-pressed={direction === 'request'}
              onClick={() => setDirection('request')}
            >
              {t('users.activity_request')}
            </Button>
            <Button
              variant={direction === 'response' ? 'primary' : 'secondary'}
              size="sm"
              aria-pressed={direction === 'response'}
              onClick={() => setDirection('response')}
            >
              {t('users.activity_response')}
            </Button>
          </div>
          <ContentInspector key={requestId + direction} detail={detail} direction={direction} />
        </div>
      )}
    </Sheet>
  );
}

function ContentInspector({
  detail,
  direction,
}: {
  detail: ActivityRequest;
  direction: ActivityDirection;
}) {
  const { t, i18n } = useTranslation();
  const { current, signal } = useSessionScope();
  const [chunks, setChunks] = useState<ActivityChunk[]>([]);
  const [next, setNext] = useState(0);
  const [complete, setComplete] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [raw, setRaw] = useState(false);
  const sequence = useRef(0);
  const load = useCallback(
    async (after = 0) => {
      const generation = ++sequence.current;
      setLoading(true);
      setError('');
      try {
        const result = await activityApi.content(detail.id, direction, after, signal());
        if (!current() || sequence.current !== generation) return;
        setChunks((previous) => appendActivityChunks(after ? previous : [], result.chunks));
        setNext(result.nextAfter);
        setComplete(result.complete);
      } catch (failure) {
        if (current() && sequence.current === generation)
          setError(getErrorMessage(failure, t('users.load_failed')));
      } finally {
        if (current() && sequence.current === generation) setLoading(false);
      }
    },
    [detail.id, direction, current, signal, t]
  );
  useEffect(() => {
    void load();
  }, [load]);
  const content = useMemo(
    () => parseActivityContent(chunks, direction, complete),
    [chunks, direction, complete]
  );
  const total = direction === 'request' ? detail.requestContentBytes : detail.responseContentBytes;
  const preview =
    direction === 'request'
      ? parsePreview(detail.bodyPreview)
      : detail.responsePreview
        ? { messages: [{ role: 'assistant', content: detail.responsePreview }] }
        : null;
  const loaded = new TextEncoder().encode(content.raw).length;
  return (
    <div className={styles.inspector}>
      <div className={styles.heading}>
        <div className={styles.actions}>
          <Button
            variant={!raw ? 'primary' : 'secondary'}
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
        <span className={styles.hint}>
          {t('users.activity_loaded_bytes', {
            loaded: loaded.toLocaleString(i18n.language),
            total: total.toLocaleString(i18n.language),
          })}
        </span>
      </div>
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      {!complete && (
        <p className={styles.notice}>
          {next > 0 ? t('users.activity_partial') : t('users.activity_pending')}
        </p>
      )}
      {direction === 'request' && detail.bodyOmittedReason && (
        <p className={styles.notice}>
          {t('users.activity_omitted', { reason: detail.bodyOmittedReason })}
        </p>
      )}
      {raw ? (
        <pre className={styles.raw}>
          {content.raw ||
            (direction === 'request' ? detail.bodyPreview : detail.responsePreview) ||
            t('users.body_unavailable')}
        </pre>
      ) : (
        <Conversation body={content.body ?? preview} />
      )}
      {(!complete || error) && (
        <div className={styles.loadMore}>
          <Button variant="secondary" loading={loading} onClick={() => void load(next)}>
            {next > 0 ? t('users.activity_load_content') : t('users.activity_refresh_content')}
          </Button>
        </div>
      )}
    </div>
  );
}
