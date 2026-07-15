// Platform dashboard data is deliberately isolated behind this async adapter.
// The deterministic mock can later be replaced by one aggregate endpoint without
// changing the administrator dashboard's presentation or chart aggregation.

export interface AdminDashboardRange {
  from?: string;
  to?: string;
}

export interface AdminDashboardStats {
  activeUsers: number;
  domainAverageCodeReceiptSeconds: number;
  domainAvailableMailboxes: number;
  domainCodeReceipts: number;
  domainCodeSuccessRate: number;
  domainTotalMailboxes: number;
  microsoftAverageCodeReceiptSeconds: number;
  microsoftAvailableEmails: number;
  microsoftCodeReceipts: number;
  microsoftCodeSuccessRate: number;
  microsoftTotalEmails: number;
  newUsers: number;
  platformRevenue: number;
  rechargeAmount: number;
  refundAmount: number;
  spendAmount: number;
  successfulCodeReceipts: number;
  totalOrders: number;
  totalUsers: number;
  withdrawAmount: number;
}

export interface AdminDashboardTrendPoint {
  activeUsers: number;
  domainAverageCodeReceiptSeconds: number;
  domainAvailableMailboxes: number;
  domainCodeOrders: number;
  domainCodeSuccessRate: number;
  domainReceivedCodes: number;
  domainTotalMailboxes: number;
  label: string;
  microsoftAverageCodeReceiptSeconds: number;
  microsoftAvailableEmails: number;
  microsoftCodeOrders: number;
  microsoftCodeSuccessRate: number;
  microsoftReceivedCodes: number;
  microsoftTotalEmails: number;
  newUsers: number;
  orders: number;
  platformRevenue: number;
  rechargeAmount: number;
  refundAmount: number;
  spendAmount: number;
  successfulCodeReceipts: number;
  totalUsers: number;
  withdrawAmount: number;
}

export interface AdminDashboardRankItem {
  amount: number;
  count: number;
  name: string;
  orders: number;
  rank: number;
  successRate: number;
}

export interface AdminDashboardInventoryRankItem {
  available: number;
  consumed: number;
  name: string;
  rank: number;
}

export interface AdminDashboardData {
  projectCodeRanking: AdminDashboardRankItem[];
  projectInventoryRanking: AdminDashboardInventoryRankItem[];
  stats: AdminDashboardStats;
  trend: AdminDashboardTrendPoint[];
}

const PROJECT_NAMES = [
  "Microsoft",
  "Telegram",
  "Discord",
  "Google",
  "OpenAI",
  "TikTok",
  "Steam",
  "Instagram",
  "GitHub",
  "Facebook",
] as const;

type Random = () => number;

const HOUR_MS = 60 * 60 * 1000;
const MAX_RANGE_DAYS = 366;

interface PlatformState {
  activeUsers: number;
  domainAvailableMailboxes: number;
  domainTotalMailboxes: number;
  microsoftAvailableEmails: number;
  microsoftTotalEmails: number;
  totalUsers: number;
}

function createRandom(seed: number): Random {
  let state = seed >>> 0;
  return () => {
    state = (state * 1664525 + 1013904223) >>> 0;
    return state / 0x100000000;
  };
}

function hashText(input: string) {
  let hash = 2166136261;
  for (let index = 0; index < input.length; index += 1) {
    hash ^= input.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

function randomInt(random: Random, min: number, max: number) {
  return Math.floor(random() * (max - min + 1)) + min;
}

function roundMoney(value: number) {
  return Math.round(Math.max(0, value) * 100) / 100;
}

function roundRate(successes: number, attempts: number) {
  return attempts ? Number(((successes / attempts) * 100).toFixed(1)) : 0;
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

function parseOptionalDate(value?: string) {
  if (!value) return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed;
}

function normalizeRange(range: AdminDashboardRange, now = new Date()) {
  const parsedFrom = parseOptionalDate(range.from);
  const parsedTo = parseOptionalDate(range.to);
  const fallback = parsedFrom ?? parsedTo ?? now;
  let from = new Date((parsedFrom ?? fallback).getTime());
  let to = new Date((parsedTo ?? fallback).getTime());

  if (from.getTime() > to.getTime()) {
    [from, to] = [to, from];
  }
  if (to.getTime() > now.getTime()) to = new Date(now.getTime());
  if (from.getTime() > to.getTime()) from = new Date(to.getTime());

  const earliestAllowed = new Date(to.getTime());
  earliestAllowed.setDate(earliestAllowed.getDate() - (MAX_RANGE_DAYS - 1));
  if (from.getTime() < earliestAllowed.getTime()) from = earliestAllowed;

  return { from, to };
}

function dateKey(date: Date) {
  return [
    date.getFullYear(),
    String(date.getMonth() + 1).padStart(2, "0"),
    String(date.getDate()).padStart(2, "0"),
  ].join("-");
}

function sameDay(left: Date, right: Date) {
  return dateKey(left) === dateKey(right);
}

function trendDateLabel(date: Date, from: Date, to: Date) {
  const datePart = `${date.getMonth() + 1}/${date.getDate()}`;
  return from.getFullYear() === to.getFullYear()
    ? datePart
    : `${date.getFullYear()}/${datePart}`;
}

function createInitialState(random: Random): PlatformState {
  const microsoftTotalEmails = randomInt(random, 12_800, 16_600);
  const domainTotalMailboxes = randomInt(random, 8_200, 11_800);
  const totalUsers = randomInt(random, 6_400, 8_900);

  return {
    activeUsers: Math.round(totalUsers * (0.17 + random() * 0.09)),
    domainAvailableMailboxes: Math.round(
      domainTotalMailboxes * (0.58 + random() * 0.18),
    ),
    domainTotalMailboxes,
    microsoftAvailableEmails: Math.round(
      microsoftTotalEmails * (0.61 + random() * 0.2),
    ),
    microsoftTotalEmails,
    totalUsers,
  };
}

function hourlyWeight(hour: number) {
  if (hour < 7) return 0.24;
  if (hour < 11) return 0.8;
  if (hour < 15) return 1.24;
  if (hour < 20) return 1.08;
  return 0.58;
}

function weekdayWeight(day: number) {
  return [0.72, 1.04, 1.12, 1.09, 1.18, 0.95, 0.68][day] ?? 1;
}

function buildTrendPoint(
  random: Random,
  state: PlatformState,
  label: string,
  activityWeight: number,
  hourly: boolean,
): AdminDashboardTrendPoint {
  const microsoftCodeOrders = Math.max(
    1,
    Math.round((hourly ? 22 : 350) * activityWeight * (0.76 + random() * 0.48)),
  );
  const domainCodeOrders = Math.max(
    1,
    Math.round((hourly ? 13 : 205) * activityWeight * (0.74 + random() * 0.52)),
  );
  const microsoftReceivedCodes = Math.min(
    microsoftCodeOrders,
    Math.round(microsoftCodeOrders * (0.91 + random() * 0.075)),
  );
  const domainReceivedCodes = Math.min(
    domainCodeOrders,
    Math.round(domainCodeOrders * (0.86 + random() * 0.105)),
  );

  const microsoftRestocked = randomInt(random, hourly ? 0 : 28, hourly ? 7 : 105);
  const domainRestocked = randomInt(random, hourly ? 0 : 18, hourly ? 5 : 76);
  const microsoftUnavailable = randomInt(random, hourly ? 0 : 19, hourly ? 6 : 78);
  const domainUnavailable = randomInt(random, hourly ? 0 : 13, hourly ? 5 : 58);

  state.microsoftTotalEmails += microsoftRestocked;
  state.domainTotalMailboxes += domainRestocked;
  state.microsoftAvailableEmails = clamp(
    state.microsoftAvailableEmails + microsoftRestocked - microsoftUnavailable,
    0,
    state.microsoftTotalEmails,
  );
  state.domainAvailableMailboxes = clamp(
    state.domainAvailableMailboxes + domainRestocked - domainUnavailable,
    0,
    state.domainTotalMailboxes,
  );

  const newUsers = randomInt(random, hourly ? 0 : 12, hourly ? 4 : 46);
  state.totalUsers += newUsers;
  state.activeUsers = clamp(
    Math.round(state.totalUsers * (0.15 + random() * 0.13) * activityWeight),
    0,
    state.totalUsers,
  );

  const mailboxOrders = Math.max(
    1,
    Math.round((hourly ? 5 : 72) * activityWeight * (0.72 + random() * 0.5)),
  );
  const orders = microsoftCodeOrders + domainCodeOrders + mailboxOrders;
  const spendAmount = roundMoney(orders * (0.62 + random() * 1.28));
  const rechargeAmount = roundMoney(spendAmount * (0.88 + random() * 0.39));
  const refundAmount = roundMoney(spendAmount * (0.006 + random() * 0.027));
  const withdrawAmount = roundMoney(spendAmount * (0.08 + random() * 0.14));
  const platformRevenue = roundMoney(
    spendAmount * (0.17 + random() * 0.09) - refundAmount,
  );
  const successfulCodeReceipts = microsoftReceivedCodes + domainReceivedCodes;
  const microsoftAverageCodeReceiptSeconds = randomInt(random, 18, 52);
  const domainAverageCodeReceiptSeconds = randomInt(random, 26, 78);

  return {
    activeUsers: state.activeUsers,
    domainAverageCodeReceiptSeconds,
    domainAvailableMailboxes: state.domainAvailableMailboxes,
    domainCodeOrders,
    domainCodeSuccessRate: roundRate(domainReceivedCodes, domainCodeOrders),
    domainReceivedCodes,
    domainTotalMailboxes: state.domainTotalMailboxes,
    label,
    microsoftAverageCodeReceiptSeconds,
    microsoftAvailableEmails: state.microsoftAvailableEmails,
    microsoftCodeOrders,
    microsoftCodeSuccessRate: roundRate(
      microsoftReceivedCodes,
      microsoftCodeOrders,
    ),
    microsoftReceivedCodes,
    microsoftTotalEmails: state.microsoftTotalEmails,
    newUsers,
    orders,
    platformRevenue,
    rechargeAmount,
    refundAmount,
    spendAmount,
    successfulCodeReceipts,
    totalUsers: state.totalUsers,
    withdrawAmount,
  };
}

function buildTrend(from: Date, to: Date, random: Random) {
  const state = createInitialState(random);

  if (sameDay(from, to)) {
    const cursor = new Date(from.getTime());
    cursor.setMinutes(0, 0, 0);
    const trend: AdminDashboardTrendPoint[] = [];

    while (cursor.getTime() <= to.getTime()) {
      const hour = cursor.getHours();
      const bucketStart = Math.max(cursor.getTime(), from.getTime());
      const bucketEnd = Math.min(cursor.getTime() + HOUR_MS, to.getTime() + 1);
      const coverage = Math.max(0, bucketEnd - bucketStart) / HOUR_MS;
      trend.push(
        buildTrendPoint(
          random,
          state,
          `${String(hour).padStart(2, "0")}:00`,
          hourlyWeight(hour) * coverage,
          true,
        ),
      );
      cursor.setTime(cursor.getTime() + HOUR_MS);
    }

    return trend;
  }

  const cursor = new Date(from.getTime());
  cursor.setHours(0, 0, 0, 0);
  const end = new Date(to.getTime());
  end.setHours(0, 0, 0, 0);
  const trend: AdminDashboardTrendPoint[] = [];

  while (cursor.getTime() <= end.getTime()) {
    const nextDay = new Date(cursor.getTime());
    nextDay.setDate(nextDay.getDate() + 1);
    const bucketStart = Math.max(cursor.getTime(), from.getTime());
    const bucketEnd = Math.min(nextDay.getTime(), to.getTime() + 1);
    const coverage =
      Math.max(0, bucketEnd - bucketStart) /
      (nextDay.getTime() - cursor.getTime());
    trend.push(
      buildTrendPoint(
        random,
        state,
        trendDateLabel(cursor, from, to),
        weekdayWeight(cursor.getDay()) * coverage,
        false,
      ),
    );
    cursor.setDate(cursor.getDate() + 1);
  }

  return trend;
}

function distributeWholeNumber(total: number, weights: number[]) {
  const safeTotal = Math.max(0, Math.round(total));
  const weightSum = weights.reduce((sum, weight) => sum + weight, 0) || 1;
  const rawValues = weights.map((weight) => (safeTotal * weight) / weightSum);
  const values = rawValues.map(Math.floor);
  const remainder = safeTotal - values.reduce((sum, value) => sum + value, 0);
  const remainderOrder = rawValues
    .map((value, index) => ({ fraction: value - Math.floor(value), index }))
    .sort((left, right) => right.fraction - left.fraction || left.index - right.index);

  for (let index = 0; index < remainder; index += 1) {
    values[remainderOrder[index % remainderOrder.length]!.index]! += 1;
  }

  return values;
}

function buildRanking(
  random: Random,
  names: readonly string[],
  totalCount: number,
  totalOrders: number,
  totalAmount: number,
  coverage: number,
): AdminDashboardRankItem[] {
  const weights = names.map((_, index) =>
    Math.max(0.72, names.length - index * 0.7 + random() * 0.85),
  );
  const coveredCount = Math.floor(totalCount * coverage);
  const coveredOrders = Math.max(coveredCount, Math.floor(totalOrders * coverage));
  const coveredAmountInCents = Math.floor(totalAmount * 100 * coverage);
  const counts = distributeWholeNumber(coveredCount, weights);
  const orders = distributeWholeNumber(coveredOrders, weights);
  const amounts = distributeWholeNumber(coveredAmountInCents, weights);

  return names
    .map((name, index) => {
      const count = counts[index] ?? 0;
      const orderCount = Math.max(count, orders[index] ?? 0);
      return {
        amount: (amounts[index] ?? 0) / 100,
        count,
        name,
        orders: orderCount,
        rank: 0,
        successRate: roundRate(count, orderCount),
      };
    })
    .sort((left, right) => right.count - left.count || right.amount - left.amount)
    .map((item, index) => ({ ...item, rank: index + 1 }));
}

function buildProjectInventoryRanking(
  random: Random,
  projectCodeRanking: AdminDashboardRankItem[],
): AdminDashboardInventoryRankItem[] {
  let available = randomInt(random, 18, 72);

  return projectCodeRanking
    .map((project, index) => {
      if (index > 0) available += randomInt(random, 24, 118);
      return {
        available,
        consumed: project.count,
        name: project.name,
        rank: 0,
      };
    })
    .sort(
      (left, right) =>
        left.available - right.available || right.consumed - left.consumed,
    )
    .map((item, index) => ({ ...item, rank: index + 1 }));
}

function sumTrend(
  trend: AdminDashboardTrendPoint[],
  field: keyof AdminDashboardTrendPoint,
) {
  return trend.reduce((sum, point) => {
    const value = point[field];
    return sum + (typeof value === "number" ? value : 0);
  }, 0);
}

function simulateLatency(ms = 60) {
  return new Promise<void>((resolve) => globalThis.setTimeout(resolve, ms));
}

export async function getAdminDashboardData(
  range: AdminDashboardRange = {},
): Promise<AdminDashboardData> {
  await simulateLatency();

  const now = new Date();
  now.setSeconds(0, 0);
  const { from, to } = normalizeRange(range, now);
  const random = createRandom(
    hashText(`admin-dashboard-v2|${from.toISOString()}|${to.toISOString()}`),
  );
  const trend = buildTrend(from, to, random);
  const lastPoint = trend[trend.length - 1]!;
  const rechargeAmount = roundMoney(sumTrend(trend, "rechargeAmount"));
  const spendAmount = roundMoney(sumTrend(trend, "spendAmount"));
  const refundAmount = roundMoney(sumTrend(trend, "refundAmount"));
  const withdrawAmount = roundMoney(sumTrend(trend, "withdrawAmount"));
  const platformRevenue = roundMoney(sumTrend(trend, "platformRevenue"));
  const totalOrders = sumTrend(trend, "orders");
  const successfulCodeReceipts = sumTrend(trend, "successfulCodeReceipts");
  const microsoftCodeOrders = sumTrend(trend, "microsoftCodeOrders");
  const microsoftCodeReceipts = sumTrend(trend, "microsoftReceivedCodes");
  const domainCodeOrders = sumTrend(trend, "domainCodeOrders");
  const domainCodeReceipts = sumTrend(trend, "domainReceivedCodes");
  const microsoftAverageCodeReceiptSeconds = microsoftCodeReceipts
    ? Math.round(
        trend.reduce(
          (sum, point) =>
            sum +
            point.microsoftAverageCodeReceiptSeconds * point.microsoftReceivedCodes,
          0,
        ) / microsoftCodeReceipts,
      )
    : 0;
  const domainAverageCodeReceiptSeconds = domainCodeReceipts
    ? Math.round(
        trend.reduce(
          (sum, point) =>
            sum + point.domainAverageCodeReceiptSeconds * point.domainReceivedCodes,
          0,
        ) / domainCodeReceipts,
      )
    : 0;
  const stats: AdminDashboardStats = {
    activeUsers: lastPoint.activeUsers,
    domainAverageCodeReceiptSeconds,
    domainAvailableMailboxes: lastPoint.domainAvailableMailboxes,
    domainCodeReceipts,
    domainCodeSuccessRate: roundRate(domainCodeReceipts, domainCodeOrders),
    domainTotalMailboxes: lastPoint.domainTotalMailboxes,
    microsoftAverageCodeReceiptSeconds,
    microsoftAvailableEmails: lastPoint.microsoftAvailableEmails,
    microsoftCodeReceipts,
    microsoftCodeSuccessRate: roundRate(
      microsoftCodeReceipts,
      microsoftCodeOrders,
    ),
    microsoftTotalEmails: lastPoint.microsoftTotalEmails,
    newUsers: sumTrend(trend, "newUsers"),
    platformRevenue,
    rechargeAmount,
    refundAmount,
    spendAmount,
    successfulCodeReceipts,
    totalOrders,
    totalUsers: lastPoint.totalUsers,
    withdrawAmount,
  };
  const totalCodeOrders = microsoftCodeOrders + domainCodeOrders;
  const projectCodeRanking = buildRanking(
    random,
    PROJECT_NAMES,
    successfulCodeReceipts,
    totalCodeOrders,
    spendAmount,
    1,
  );

  return {
    projectCodeRanking,
    projectInventoryRanking: buildProjectInventoryRanking(random, projectCodeRanking),
    stats,
    trend,
  };
}
