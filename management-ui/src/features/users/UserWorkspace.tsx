import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { usersApi, type ManagedUser } from '@/services/api/users';
import { getErrorMessage } from '@/utils/helpers';
import { ActivityPanel } from './ActivityPanel';
import { AllowedModelsDialog } from './AllowedModelsDialog';
import { BudgetPanel } from './BudgetPanel';
import { KeysDialog } from './KeysDialog';
import { RequestInspector } from './RequestInspector';
import { useSessionScope } from './useSessionScope';
import styles from './UsersPage.module.scss';

export function UserWorkspace({
  initialUser,
  refresh,
  onUpdated,
}: {
  initialUser: ManagedUser;
  refresh: number;
  onUpdated: (user: ManagedUser) => void;
}) {
  const { t } = useTranslation();
  const { current, signal } = useSessionScope();
  const [user, setUser] = useState(initialUser);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [keysOpen, setKeysOpen] = useState(false);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [change, setChange] = useState(0);
  const [requestId, setRequestId] = useState('');
  const [sessionId, setSessionId] = useState('');
  const sequence = useRef(0);
  const id = initialUser.id;
  useEffect(() => {
    const request = ++sequence.current;
    setLoading(true);
    setError('');
    void usersApi
      .get(id, signal())
      .then((value) => {
        if (!current() || request !== sequence.current) return;
        setUser(value);
        onUpdated(value);
      })
      .catch((failure) => {
        if (current() && request === sequence.current)
          setError(getErrorMessage(failure, t('users.load_failed')));
      })
      .finally(() => {
        if (current() && request === sequence.current) setLoading(false);
      });
  }, [id, refresh, current, signal, onUpdated, t]);
  return (
    <>
      <section className={styles.account} aria-busy={loading}>
        <div className={styles.sectionHeading}>
          <div>
            <div className={styles.accountTitle}>
              <h2>{user.displayName || user.email}</h2>
              <span className={styles.badge}>
                {t(
                  user.systemAdmin
                    ? 'users.system_admin'
                    : user.status === 'active'
                      ? 'users.active'
                      : 'users.disabled'
                )}
              </span>
            </div>
            <p className={styles.description}>{user.email}</p>
          </div>
          <div className={styles.actions}>
            <Button
              variant="secondary"
              size="sm"
              disabled={loading || user.systemAdmin}
              onClick={() => setModelsOpen(true)}
            >
              {t('users.allowed_models')}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => setKeysOpen(true)}>
              {t('users.api_keys')}
            </Button>
          </div>
        </div>
        {user.systemAdmin && <p className={styles.hint}>{t('users.configured_credential')}</p>}
        {error && (
          <div className="error-box" role="alert">
            {error}
          </div>
        )}
      </section>
      <BudgetPanel
        userId={id}
        adminExempt={user.systemAdmin}
        refreshKey={refresh + change}
        onChanged={() => setChange((value) => value + 1)}
        onInspectRequest={setRequestId}
      />
      <ActivityPanel
        userId={id}
        refreshKey={`${refresh}:${change}`}
        sessionId={sessionId}
        onSessionChange={setSessionId}
      />
      {requestId && (
        <RequestInspector
          key={requestId}
          requestId={requestId}
          onClose={() => setRequestId('')}
          onSession={(value) => {
            setSessionId(value);
            setRequestId('');
          }}
        />
      )}
      {keysOpen && <KeysDialog user={user} onClose={() => setKeysOpen(false)} />}
      {modelsOpen && <AllowedModelsDialog userId={id} onClose={() => setModelsOpen(false)} />}
    </>
  );
}
