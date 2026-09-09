import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuthStore } from '@/stores/useAuthStore';

/** Every async continuation checks the active connection before touching UI state. */
export function useSessionScope() {
  const [identity] = useState(() => {
    const { apiBase, managementKey } = useAuthStore.getState();
    return { apiBase, managementKey };
  });
  const live = useRef(false);
  const controller = useRef(new AbortController());
  useEffect(() => {
    controller.current = new AbortController();
    live.current = true;
    const unsubscribe = useAuthStore.subscribe((auth) => {
      if (
        !auth.isAuthenticated ||
        auth.connectionStatus !== 'connected' ||
        auth.apiBase !== identity.apiBase ||
        auth.managementKey !== identity.managementKey
      ) {
        live.current = false;
        controller.current.abort();
      }
    });
    return () => {
      unsubscribe();
      live.current = false;
      controller.current.abort();
    };
  }, [identity]);
  const current = useCallback(() => {
    const auth = useAuthStore.getState();
    return (
      live.current &&
      auth.isAuthenticated &&
      auth.connectionStatus === 'connected' &&
      auth.apiBase === identity.apiBase &&
      auth.managementKey === identity.managementKey
    );
  }, [identity]);
  const signal = useCallback(() => controller.current.signal, []);
  return { current, signal };
}
