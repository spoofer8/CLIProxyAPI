import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { IconPlus, IconRefreshCw, IconSearch, IconSettings } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useAuthStore } from '@/stores/useAuthStore';
import { useNotificationStore } from '@/stores/useNotificationStore';
import { usersApi, type ManagedUser } from '@/services/api/users';
import { getErrorMessage } from '@/utils/helpers';
import { UserWorkspace } from './UserWorkspace';
import { PricingDialog } from './PricingDialog';
import { useSessionScope } from './useSessionScope';
import styles from './UsersPage.module.scss';

export function UsersPage() {
  const { t } = useTranslation();
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const connected = useAuthStore(
    (state) => state.isAuthenticated && state.connectionStatus === 'connected'
  );
  if (!connected) return <p className={styles.empty}>{t('notification.connection_required')}</p>;
  return <UsersSession key={JSON.stringify([apiBase, managementKey])} />;
}

function UsersSession() {
  const { t } = useTranslation();
  const { current, signal } = useSessionScope();
  const notify = useNotificationStore((state) => state.showNotification);
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [total, setTotal] = useState(0);
  const [nextOffset, setNextOffset] = useState(0);
  const [selected, setSelected] = useState('');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [dialog, setDialog] = useState<'create' | 'pricing' | null>(null);
  const [formError, setFormError] = useState('');
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const sequence = useRef(0);

  const load = useCallback(
    async (offset = 0) => {
      if (!current()) return;
      const request = ++sequence.current;
      setLoading(true);
      setError('');
      try {
        const people = await usersApi.list(offset, signal());
        if (!current() || request !== sequence.current) return;
        setUsers((previous) =>
          offset
            ? [...new Map([...previous, ...people.users].map((user) => [user.id, user])).values()]
            : people.users
        );
        setTotal(people.total);
        setNextOffset(offset + people.users.length);
        setSelected((previous) =>
          offset || people.users.some((user) => user.id === previous)
            ? previous
            : (people.users[0]?.id ?? '')
        );
      } catch (failure) {
        if (!current() || request !== sequence.current) return;
        const status = (failure as { status?: number })?.status;
        setError(
          status === 404 ? t('users.unsupported') : getErrorMessage(failure, t('users.load_failed'))
        );
      }
      setLoading(false);
    },
    [current, signal, t]
  );

  useEffect(() => {
    void load();
  }, [load]);
  const reload = useCallback(async () => {
    setRefresh((value) => value + 1);
    await load();
  }, [load]);
  useHeaderRefresh(reload);

  const visibleUsers = useMemo(() => {
    const query = search.trim().toLowerCase();
    return users.filter((user) =>
      (user.displayName + ' ' + user.email).toLowerCase().includes(query)
    );
  }, [search, users]);
  const selectedUser = users.find((user) => user.id === selected);
  const updateUser = useCallback((updated: ManagedUser) => {
    setUsers((previous) =>
      previous.map((user) => (user.id === updated.id ? { ...user, ...updated } : user))
    );
  }, []);

  const open = (next: 'create' | 'pricing') => {
    setFormError('');
    setDialog(next);
  };

  const save = async () => {
    if (!current() || busy) return;
    setFormError('');
    setBusy(true);
    try {
      {
        const user = await usersApi.create(name, email, signal());
        if (!current()) return;
        // Keep the server offset separate from a newly created user pinned into this list.
        await load();
        if (!current()) return;
        setUsers((previous) =>
          previous.some((item) => item.id === user.id) ? previous : [...previous, user]
        );
        setSelected(user.id);
        setSearch('');
        setName('');
        setEmail('');
        notify(t('users.user_created'), 'success');
      }
      setDialog(null);
    } catch (failure) {
      if (current()) setFormError(getErrorMessage(failure, t('users.action_failed')));
    } finally {
      if (current()) setBusy(false);
    }
  };

  return (
    <div className={styles.page}>
      <div className={styles.pageHeader}>
        <div>
          <h1 className={styles.title}>{t('users.title')}</h1>
          <p className={styles.description}>{t('users.description')}</p>
        </div>
        <div className={styles.actions}>
          <Button variant="secondary" onClick={() => open('pricing')}>
            <IconSettings size={16} />
            {t('users.finance.pricing_title')}
          </Button>
          <Button onClick={() => open('create')}>
            <IconPlus size={16} />
            {t('users.new_user')}
          </Button>
        </div>
      </div>
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
      <div className={styles.workspace}>
        <section className={styles.userPanel} aria-label={t('users.search')}>
          <div className={styles.userToolbar}>
            <Input
              aria-label={t('users.search')}
              placeholder={t('users.search')}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              rightElement={<IconSearch size={16} />}
            />
            <div className={styles.sectionHeading}>
              <span className={styles.hint}>
                {t('users.users_count', { loaded: users.length, total })}
              </span>
              <Button
                variant="ghost"
                size="sm"
                aria-label={t('common.refresh')}
                onClick={() => void reload()}
                disabled={loading}
              >
                <IconRefreshCw size={16} />
              </Button>
            </div>
          </div>
          <div className={styles.userList} aria-busy={loading}>
            {visibleUsers.map((user) => (
              <button
                key={user.id}
                type="button"
                className={[styles.userItem, selected === user.id ? styles.userSelected : ''].join(
                  ' '
                )}
                aria-pressed={selected === user.id}
                onClick={() => setSelected(user.id)}
              >
                <span className={styles.userName}>
                  {user.displayName || user.email}
                  <span
                    className={user.status === 'active' ? styles.activeDot : styles.disabledDot}
                    aria-label={t(user.status === 'active' ? 'users.active' : 'users.disabled')}
                  />
                </span>
                <span className={styles.userEmail}>{user.email}</span>
                <span className={styles.userQuota}>
                  {t(
                    user.systemAdmin
                      ? 'users.system_admin'
                      : user.role === 'admin'
                        ? 'users.administrator'
                        : 'users.standard_user'
                  )}
                </span>
              </button>
            ))}
            {!visibleUsers.length && (
              <p className={styles.empty}>
                {loading
                  ? t('common.loading')
                  : search
                    ? t('users.no_matches')
                    : t('users.no_users')}
              </p>
            )}
          </div>
          {nextOffset < total && (
            <div className={styles.panelFooter}>
              <Button
                variant="secondary"
                size="sm"
                fullWidth
                loading={loading}
                onClick={() => void load(nextOffset)}
              >
                {t('users.load_more_users')}
              </Button>
            </div>
          )}
        </section>
        <div className={styles.userContent}>
          {selectedUser ? (
            <UserWorkspace
              key={selectedUser.id}
              initialUser={selectedUser}
              refresh={refresh}
              onUpdated={updateUser}
            />
          ) : (
            <div className={styles.emptyPanel}>
              {loading ? t('common.loading') : t('users.select_user')}
            </div>
          )}
        </div>
      </div>
      <Modal
        open={dialog === 'create'}
        onClose={() => setDialog(null)}
        closeDisabled={busy}
        title={t('users.create_user')}
        footer={
          <>
            <Button variant="secondary" disabled={busy} onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="users-page-form" loading={busy}>
              {t('users.create_user')}
            </Button>
          </>
        }
      >
        <form
          id="users-page-form"
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <>
            <Input
              label={t('users.display_name')}
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={200}
            />
            <Input
              label={t('users.email')}
              type="email"
              required
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              maxLength={254}
            />
          </>
          {formError && (
            <div className="error-box" role="alert">
              {formError}
            </div>
          )}
        </form>
      </Modal>
      {dialog === 'pricing' && <PricingDialog open onClose={() => setDialog(null)} />}
    </div>
  );
}
