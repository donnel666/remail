import { useCallback, useEffect, useRef } from "react";
import { Modal } from "@douyinfe/semi-ui";

import { useAuth } from "@/context/auth-provider";
import { csrfHeader } from "@/lib/api-client";

export function usePageConfirmation() {
  const { currentUser } = useAuth();
  const userId = currentUser?.id;
  const currentUserId = useRef(userId);
  currentUserId.current = userId;
  const mounted = useRef(false);
  const cancellations = useRef(new Set<() => void>());

  useEffect(() => {
    mounted.current = true;
    const pending = cancellations.current;
    return () => {
      mounted.current = false;
      for (const cancel of pending) cancel();
      pending.clear();
    };
  }, [userId]);

  return useCallback((props: Parameters<typeof Modal.confirm>[0]) => {
    if (!mounted.current || currentUserId.current !== userId) return;
    const csrfToken = csrfHeader()["X-CSRF-Token"];
    let active = true;
    const modal = Modal.confirm({
      ...props,
      onOk: (event) => {
        if (!active || !mounted.current || currentUserId.current !== userId || csrfHeader()["X-CSRF-Token"] !== csrfToken) return;
        return props.onOk?.(event);
      },
      afterClose: () => {
        active = false;
        cancellations.current.delete(cancel);
        props.afterClose?.();
      },
    });
    const cancel = () => {
      active = false;
      modal.destroy();
    };
    cancellations.current.add(cancel);
    return modal;
  }, [userId]);
}
