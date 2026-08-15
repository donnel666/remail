import { useEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { Select } from "@douyinfe/semi-ui";

import type { CurrentUser } from "@/context/auth-provider";
import { SHARED_SEARCH_DEBOUNCE_MS } from "@/hooks/use-debounced-value";

export interface AdminUserSelectOption<T = unknown> {
  data?: T;
  disabled?: boolean;
  label: string;
  value: number;
}

interface AdminResourceOwner {
  email: string;
  enabled: boolean;
  groupName: string;
  id: number;
  nickname: string;
  role: CurrentUser["role"];
}

type CurrentOwnerUser = Pick<
  CurrentUser,
  "email" | "enabled" | "id" | "nickname" | "role"
> & {
  userGroup: Pick<CurrentUser["userGroup"], "name">;
};

export function ownersWithCurrentUserFirst(
  owners: AdminResourceOwner[],
  currentUser: CurrentOwnerUser | null
): AdminResourceOwner[] {
  if (!currentUser) return owners;
  return [
    {
      email: currentUser.email,
      enabled: currentUser.enabled,
      groupName: currentUser.userGroup.name,
      id: currentUser.id,
      nickname: currentUser.nickname,
      role: currentUser.role,
    },
    ...owners.filter((owner) => owner.id !== currentUser.id),
  ];
}

const EMPTY_OPTIONS: AdminUserSelectOption<never>[] = [];

interface AdminUserSelectProps<T> {
  emptyContent: ReactNode;
  loadOptions: (keyword: string) => Promise<AdminUserSelectOption<T>[]>;
  onChange: (
    value: number | undefined,
    option: AdminUserSelectOption<T> | undefined
  ) => void;
  onLoadError?: (error: unknown) => void;
  options?: AdminUserSelectOption<T>[];
  placeholder: ReactNode;
  selectedOption?: AdminUserSelectOption<T>;
  showClear?: boolean;
  style?: CSSProperties;
  value?: number;
}

function valueAsUserID(value: unknown) {
  const userID = Number(value);
  return Number.isInteger(userID) && userID > 0 ? userID : undefined;
}

function keepSelectedOption<T>(
  options: AdminUserSelectOption<T>[],
  selected: AdminUserSelectOption<T> | undefined
) {
  if (!selected || options.some((option) => option.value === selected.value)) {
    return options;
  }
  return [selected, ...options];
}

export function AdminUserSelect<T>({
  emptyContent,
  loadOptions,
  onChange,
  onLoadError,
  options: providedOptions,
  placeholder,
  selectedOption,
  showClear,
  style,
  value,
}: AdminUserSelectProps<T>) {
  const options =
    providedOptions ?? (EMPTY_OPTIONS as AdminUserSelectOption<T>[]);
  const initialSelected =
    selectedOption?.value === value
      ? selectedOption
      : options.find((option) => option.value === value);
  const selectedRef = useRef(initialSelected);
  const [visibleOptions, setVisibleOptions] = useState(() =>
    keepSelectedOption(options, initialSelected)
  );
  const [loading, setLoading] = useState(false);
  const requestSequence = useRef(0);
  const searchDebounce = useRef<ReturnType<typeof globalThis.setTimeout> | null>(
    null
  );

  useEffect(() => {
    const selected =
      (selectedOption?.value === value ? selectedOption : undefined) ??
      options.find((option) => option.value === value) ??
      (selectedRef.current?.value === value ? selectedRef.current : undefined);
    selectedRef.current = selected;
    setVisibleOptions((current) =>
      keepSelectedOption(
        providedOptions === undefined ? current : options,
        selected
      )
    );
  }, [options, providedOptions, selectedOption, value]);

  useEffect(
    () => () => {
      if (searchDebounce.current) {
        globalThis.clearTimeout(searchDebounce.current);
      }
      requestSequence.current += 1;
    },
    []
  );

  const search = async (keyword: string) => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    try {
      const result = await loadOptions(keyword);
      if (requestSequence.current === sequence) {
        setVisibleOptions(keepSelectedOption(result, selectedRef.current));
      }
    } catch (error) {
      if (requestSequence.current === sequence) onLoadError?.(error);
    } finally {
      if (requestSequence.current === sequence) setLoading(false);
    }
  };

  const queueSearch = (keyword: string) => {
    if (searchDebounce.current) globalThis.clearTimeout(searchDebounce.current);
    searchDebounce.current = globalThis.setTimeout(() => {
      void search(keyword);
    }, SHARED_SEARCH_DEBOUNCE_MS);
  };

  const rememberSelected = (next: unknown) => {
    const userID = valueAsUserID(next);
    selectedRef.current = visibleOptions.find(
      (option) => option.value === userID
    );
  };

  return (
    <Select
      emptyContent={emptyContent}
      filter
      loading={loading}
      onChange={(next) => {
        const userID = valueAsUserID(next);
        const selected =
          visibleOptions.find((option) => option.value === userID) ??
          (selectedRef.current?.value === userID
            ? selectedRef.current
            : undefined);
        selectedRef.current = selected;
        onChange(userID, selected);
      }}
      onDropdownVisibleChange={(visible) => {
        if (visible && visibleOptions.length === 0) void search("");
      }}
      onSearch={queueSearch}
      onSelect={rememberSelected}
      placeholder={placeholder}
      remote
      optionList={visibleOptions}
      renderSelectedItem={(option: { label?: ReactNode; value?: unknown }) => {
        const selected = selectedRef.current;
        return selected && selected.value === option.value
          ? selected.label
          : option.label;
      }}
      searchPosition="dropdown"
      showClear={showClear}
      style={style}
      value={value}
    />
  );
}
