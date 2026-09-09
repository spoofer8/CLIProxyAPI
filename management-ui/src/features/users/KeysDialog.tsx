import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { usersApi, type ManagedUser, type UserApiKey } from '@/services/api/users';
import { useNotificationStore } from '@/stores/useNotificationStore';
import { getErrorMessage } from '@/utils/helpers';
import { useSessionScope } from './useSessionScope';
import styles from './UsersPage.module.scss';

export function KeysDialog({ user, onClose }: { user: ManagedUser; onClose: () => void }) {
  const { t } = useTranslation();
  const { current, signal } = useSessionScope();
  const notify = useNotificationStore((state) => state.showNotification);
  const [keys, setKeys] = useState<UserApiKey[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [label, setLabel] = useState('');
  const [secret, setSecret] = useState('');
  const [confirmKey, setConfirmKey] = useState('');
  const sequence = useRef(0);

  const load = useCallback(
    async (offset = 0) => {
      const request = ++sequence.current;
      setLoading(true);
      try {
        const response = await usersApi.keys(user.id, offset, signal());
        if (!current() || sequence.current !== request) return;
        setKeys((previous) => (offset ? [...previous, ...response.keys] : response.keys));
        setTotal(response.total);
      } catch (failure) {
        if (current() && sequence.current === request)
          setError(getErrorMessage(failure, t('users.load_failed')));
      } finally {
        if (current() && sequence.current === request) setLoading(false);
      }
    },
    [current, signal, t, user.id]
  );
  useEffect(() => {
    void load();
  }, [load]);

  const mutate = async (keyId?: string) => {
    if (!current() || busy) return;
    setBusy(true);
    setError('');
    try {
      if (keyId) {
        await usersApi.revokeKey(keyId, signal());
        if (!current()) return;
        setConfirmKey('');
        notify(t('users.key_revoked'), 'success');
      } else {
        const issued = await usersApi.issueKey(user.id, label, signal());
        if (!current()) return;
        setSecret(issued.secret);
        setLabel('');
      }
      await load();
    } catch (failure) {
      if (current()) setError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (current()) setBusy(false);
    }
  };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret);
      if (current()) notify(t('users.key_copied'), 'success');
    } catch {
      if (current()) setError(t('users.copy_failed'));
    }
  };

  return (
    <Modal
      open
      title={t('users.api_keys')}
      closeDisabled={busy}
      onClose={() => {
        setSecret('');
        onClose();
      }}
      width={620}
      footer={
        <Button
          variant="secondary"
          disabled={busy}
          onClick={() => {
            setSecret('');
            onClose();
          }}
        >
          {t('common.close')}
        </Button>
      }
    >
      <div className={styles.keysContent}>
        <p className={styles.description}>{user.displayName || user.email}</p>
        {error && (
          <div className="error-box" role="alert">
            {error}
          </div>
        )}
        <div aria-busy={loading}>
          {keys.map((key) => (
            <div key={key.id} className={styles.keyRow}>
              <div className={styles.sectionHeading}>
                <div>
                  <strong>{key.label || t('users.untitled_key')}</strong>
                  <p className={styles.hint}>
                    {key.prefix}… · {t(key.status === 'active' ? 'users.active' : 'users.revoked')}
                  </p>
                </div>
                {key.status === 'active' && confirmKey !== key.id && (
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={busy || loading}
                    onClick={() => setConfirmKey(key.id)}
                  >
                    {t('users.revoke_key')}
                  </Button>
                )}
              </div>
              {confirmKey === key.id && (
                <div>
                  <p>{t('users.revoke_confirm')}</p>
                  <div className={styles.actions}>
                    <Button
                      variant="danger"
                      size="sm"
                      loading={busy}
                      onClick={() => void mutate(key.id)}
                    >
                      {t('users.revoke_key')}
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={busy}
                      onClick={() => setConfirmKey('')}
                    >
                      {t('common.cancel')}
                    </Button>
                  </div>
                </div>
              )}
            </div>
          ))}
          {!keys.length && (
            <p className={styles.empty}>{loading ? t('common.loading') : t('users.no_keys')}</p>
          )}
          {keys.length < total && (
            <Button
              variant="secondary"
              size="sm"
              loading={loading}
              disabled={busy}
              onClick={() => void load(keys.length)}
            >
              {t('users.load_more_keys')}
            </Button>
          )}
        </div>
        {secret && (
          <div className={styles.secret}>
            <p>{t('users.key_once')}</p>
            <code>{secret}</code>
            <Button variant="secondary" size="sm" onClick={() => void copy()}>
              {t('users.copy_key')}
            </Button>
          </div>
        )}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void mutate();
          }}
          className={styles.keyForm}
        >
          <Input
            label={t('users.key_label')}
            value={label}
            maxLength={200}
            onChange={(event) => setLabel(event.target.value)}
          />
          <Button type="submit" loading={busy} disabled={loading}>
            {t('users.issue_key')}
          </Button>
        </form>
      </div>
    </Modal>
  );
}
