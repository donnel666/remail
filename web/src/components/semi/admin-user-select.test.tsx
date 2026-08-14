// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/hooks/use-debounced-value", () => ({
  SHARED_SEARCH_DEBOUNCE_MS: 1,
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const ReactModule = await import("react");
  const Select = ({
    onChange,
    onDropdownVisibleChange,
    onSearch,
    onSelect,
    optionList = [],
    renderSelectedItem,
    value,
  }: any) => {
    const [hasSearch, setHasSearch] = ReactModule.useState(false);
    const options = optionList as Array<{ label: string; value: number }>;
    const selected =
      options.find((option: { value: number }) => option.value === value) ??
      {
        label: value,
        value,
      };

    return (
      <div>
        <output data-testid="selected-label">
          {value === undefined
            ? ""
            : renderSelectedItem?.(selected) ?? selected.label}
        </output>
        <button onClick={() => onDropdownVisibleChange?.(true)} type="button">
          Open users
        </button>
        <button
          onClick={() => {
            setHasSearch(true);
            onSearch?.("remote");
          }}
          type="button"
        >
          Search remote user
        </button>
        {options.map((option: { label: string; value: number }) => (
          <button
            key={option.value}
            onClick={() => {
              onSelect?.(option.value, option);
              if (hasSearch) {
                setHasSearch(false);
                onSearch?.("");
              }
              onChange?.(option.value);
            }}
            type="button"
          >
            {option.label}
          </button>
        ))}
      </div>
    );
  };
  return { Select };
});

import {
  AdminUserSelect,
  type AdminUserSelectOption,
} from "./admin-user-select";

const initialOption: AdminUserSelectOption = {
  label: "first@example.com · First",
  value: 1,
};
const remoteOption: AdminUserSelectOption = {
  label: "remote@example.com · Remote",
  value: 200,
};
const siblingOption = {
  label: "second@example.com · Second",
  value: 2,
} satisfies AdminUserSelectOption<never>;

describe("AdminUserSelect", () => {
  afterEach(cleanup);

  it("keeps the selected label when the cleared search no longer returns that user", async () => {
    const loadOptions = vi.fn(async (keyword: string) =>
      keyword ? [remoteOption] : [initialOption]
    );

    function Harness() {
      const [value, setValue] = useState<number>();
      return (
        <AdminUserSelect
          emptyContent="No users"
          loadOptions={loadOptions}
          onChange={setValue}
          options={[initialOption]}
          placeholder="Search users"
          value={value}
        />
      );
    }

    render(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "Search remote user" }));
    await screen.findByRole("button", { name: remoteOption.label });

    fireEvent.click(screen.getByRole("button", { name: remoteOption.label }));

    await waitFor(() =>
      expect(screen.getByTestId("selected-label")).toHaveTextContent(
        remoteOption.label
      )
    );
    await waitFor(() => expect(loadOptions).toHaveBeenLastCalledWith(""));
    expect(
      screen.getByRole("button", { name: remoteOption.label })
    ).toBeInTheDocument();
  });

  it("keeps default options and selected data when no search was entered", async () => {
    const selectedData = { email: "first@example.com", id: 1 };
    const firstOption: AdminUserSelectOption<typeof selectedData> = {
      ...initialOption,
      data: selectedData,
    };
    const defaultOptions: AdminUserSelectOption<typeof selectedData>[] = [
      firstOption,
      { ...siblingOption },
    ];
    const loadOptions = vi.fn(async (_keyword: string) => defaultOptions);
    const onSelected = vi.fn();

    function Harness() {
      const [value, setValue] = useState<number>();
      return (
        <AdminUserSelect<typeof selectedData>
          emptyContent="No users"
          loadOptions={loadOptions}
          onChange={(next, option) => {
            setValue(next);
            onSelected(option?.data);
          }}
          placeholder="Search users"
          value={value}
        />
      );
    }

    render(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "Open users" }));
    await screen.findByRole("button", { name: siblingOption.label });

    fireEvent.click(screen.getByRole("button", { name: firstOption.label }));
    fireEvent.click(screen.getByRole("button", { name: "Open users" }));

    expect(
      screen.getByRole("button", { name: siblingOption.label })
    ).toBeInTheDocument();
    expect(loadOptions).toHaveBeenCalledTimes(1);
    expect(loadOptions).toHaveBeenCalledWith("");
    expect(onSelected).toHaveBeenCalledWith(selectedData);
  });
});
