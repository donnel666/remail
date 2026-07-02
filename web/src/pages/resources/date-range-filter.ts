export type DateRangeValue = Date[];

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
