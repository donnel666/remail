import { useEffect, useState } from "react";

import { listUserGroups, type AdminUserGroup } from "./admin-users-api";

// Loads the user-group list once for the create/edit/profile group selects.
export function useUserGroups() {
  const [groups, setGroups] = useState<AdminUserGroup[]>([]);

  useEffect(() => {
    let cancelled = false;
    void listUserGroups()
      .then((result) => {
        if (!cancelled) setGroups(result);
      })
      .catch(() => {
        // The group list is a display aid; ignore load failures.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { groups };
}
