export type DateRangeValue = Date[];
export type DateRangePresetTranslator = (key: string) => string;
export const DATE_RANGE_DROPDOWN_CLASS = "remail-date-range-dropdown";

export function normalizeDateRangeValue(
  value?: Date | Date[] | string | string[]
): DateRangeValue {
  if (!Array.isArray(value)) return [];
  const dates = value
    .slice(0, 2)
    .map((item) => (item instanceof Date ? item : new Date(item)))
    .filter((date) => !Number.isNaN(date.getTime()));

  if (dates.length !== 2) return [];
  return sortDateRange(dates);
}

export function hasCompleteDateRange(range: DateRangeValue) {
  return range.length === 2;
}

export function createdFromISOString(range: DateRangeValue) {
  if (!hasCompleteDateRange(range)) return undefined;
  return range[0].toISOString();
}

export function createdToISOString(range: DateRangeValue) {
  if (!hasCompleteDateRange(range)) return undefined;
  return range[1].toISOString();
}

export function matchesCreatedAtRange(
  createdAt: string,
  range: DateRangeValue
) {
  if (!hasCompleteDateRange(range)) return true;

  const createdAtTime = new Date(createdAt).getTime();
  const createdFrom = createdFromISOString(range);
  const createdTo = createdToISOString(range);
  if (
    Number.isNaN(createdAtTime) ||
    createdFrom === undefined ||
    createdTo === undefined
  ) {
    return false;
  }

  return (
    createdAtTime >= new Date(createdFrom).getTime() &&
    createdAtTime <= new Date(createdTo).getTime()
  );
}

function sortDateRange(range: DateRangeValue) {
  const [left, right] = range;
  return left.getTime() <= right.getTime() ? [left, right] : [right, left];
}

export function createDateRangePresets(t: DateRangePresetTranslator) {
  return [
    {
      text: t("Today"),
      start: () => startOfDay(new Date()),
      end: () => endOfDay(new Date()),
    },
    {
      text: t("Last 7 days"),
      start: () => startOfDay(addDays(new Date(), -6)),
      end: () => endOfDay(new Date()),
    },
    {
      text: t("This week"),
      start: () => startOfWeek(new Date()),
      end: () => endOfWeek(new Date()),
    },
    {
      text: t("Last 30 days"),
      start: () => startOfDay(addDays(new Date(), -29)),
      end: () => endOfDay(new Date()),
    },
    {
      text: t("This month"),
      start: () => startOfMonth(new Date()),
      end: () => endOfMonth(new Date()),
    },
  ];
}

function startOfDay(value: Date) {
  const date = new Date(value);
  date.setHours(0, 0, 0, 0);
  return date;
}

function endOfDay(value: Date) {
  const date = new Date(value);
  date.setHours(23, 59, 59, 999);
  return date;
}

function addDays(value: Date, days: number) {
  const date = new Date(value);
  date.setDate(date.getDate() + days);
  return date;
}

function startOfWeek(value: Date) {
  const date = startOfDay(value);
  const daysSinceMonday = (date.getDay() + 6) % 7;
  return addDays(date, -daysSinceMonday);
}

function endOfWeek(value: Date) {
  return endOfDay(addDays(startOfWeek(value), 6));
}

function startOfMonth(value: Date) {
  const date = startOfDay(value);
  date.setDate(1);
  return date;
}

function endOfMonth(value: Date) {
  const date = startOfMonth(value);
  date.setMonth(date.getMonth() + 1);
  date.setDate(0);
  date.setHours(23, 59, 59, 999);
  return date;
}
