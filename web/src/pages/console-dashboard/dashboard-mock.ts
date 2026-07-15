// Dashboard data is deliberately isolated behind this async adapter. It uses
// deterministic mock data today, so ConsoleOverview can switch to a runtime
// endpoint later without changing its presentation or data aggregation.

export interface DashboardStats {
  averageCodeReceiptSeconds: number;
  codeSuccessRate: number;
  historicalSpend: number;
  todayCodeReceipts: number;
  todayOrders: number;
  totalCodeReceipts: number;
  totalOrders: number;
  walletBalance: number;
}

export interface DashboardTrendPoint {
  averageCodeReceiptSeconds: number;
  codeOrders: number;
  label: string;
  orders: number;
  purchaseOrders: number;
  receivedCodes: number;
  spend: number;
}

export interface DashboardRankItem {
  amount: number;
  count: number;
  isCurrentUser?: boolean;
  name: string;
  orders: number;
  rank: number;
}

export interface DashboardProjectSeries {
  name: string;
  receivedCodes: number[];
  spend: number[];
}

export interface DashboardData {
  codeRatio: number;
  historicalCurrentUserRank: DashboardRankItem;
  historicalCodeRanking: DashboardRankItem[];
  projectCodeRanking: DashboardRankItem[];
  projectSeries: DashboardProjectSeries[];
  purchaseRatio: number;
  stats: DashboardStats;
  todayCurrentUserRank: DashboardRankItem;
  todayCodeRanking: DashboardRankItem[];
  trend: DashboardTrendPoint[];
}

const PROJECT_NAMES = [
  "Microsoft",
  "Telegram",
  "Discord",
  "Google",
  "TikTok",
  "OpenAI",
  "Steam",
  "Instagram",
  "GitHub",
  "Facebook",
] as const;

const MOCK_USER_NAMES = [
  "donnels",
  "alex_chen",
  "mailpilot",
  "northstar",
  "bytewave",
  "kairos",
  "mason",
  "orbit",
  "aurora",
  "nova",
] as const;

type Random = () => number;

const HOUR_MS = 60 * 60 * 1000;
const MAX_RANGE_DAYS = 366;

function createRandom(seed: number): Random {
  let state = seed >>> 0;
  return () => {
    state = (state * 1664525 + 1013904223) >>> 0;
    return state / 0x100000000;
  };
}

function hashRange(from: Date, to: Date) {
  const input = `${from.toISOString()}|${to.toISOString()}`;
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

function sameDay(left: Date, right: Date) {
  return (
    left.getFullYear() === right.getFullYear() &&
    left.getMonth() === right.getMonth() &&
    left.getDate() === right.getDate()
  );
}

function hourlyWeight(hour: number) {
  if (hour < 7) return 0.2;
  if (hour < 11) return 0.75;
  if (hour < 15) return 1.25;
  if (hour < 20) return 1;
  return 0.55;
}

function buildHourlyTrend(
  random: Random,
  from: Date,
  to: Date,
): DashboardTrendPoint[] {
  const cursor = new Date(from.getTime());
  cursor.setMinutes(0, 0, 0);
  const points: DashboardTrendPoint[] = [];

  while (cursor.getTime() <= to.getTime()) {
    const hour = cursor.getHours();
    const bucketStart = Math.max(cursor.getTime(), from.getTime());
    const bucketEnd = Math.min(cursor.getTime() + HOUR_MS, to.getTime() + 1);
    const coverage = Math.max(0, bucketEnd - bucketStart) / HOUR_MS;
    const weight = hourlyWeight(hour);
    const orders = Math.max(
      1,
      Math.round((5 + random() * 24 * weight) * coverage),
    );
    const codeOrders = Math.max(1, Math.round(orders * (0.56 + random() * 0.2)));
    const receivedCodes = Math.max(
      1,
      Math.round(codeOrders * (0.82 + random() * 0.13)),
    );
    points.push({
      averageCodeReceiptSeconds: randomInt(random, 18, 72),
      codeOrders,
      label: `${String(hour).padStart(2, "0")}:00`,
      orders,
      purchaseOrders: Math.max(0, orders - codeOrders),
      receivedCodes,
      spend: roundMoney(orders * (0.72 + random() * 1.48)),
    });
    cursor.setTime(cursor.getTime() + HOUR_MS);
  }

  return points;
}

function trendDateLabel(date: Date, from: Date, to: Date) {
  const datePart = `${date.getMonth() + 1}/${date.getDate()}`;
  return from.getFullYear() === to.getFullYear()
    ? datePart
    : `${date.getFullYear()}/${datePart}`;
}

function buildDailyTrend(from: Date, to: Date, random: Random): DashboardTrendPoint[] {
  const cursor = new Date(from);
  cursor.setHours(0, 0, 0, 0);
  const end = new Date(to);
  end.setHours(0, 0, 0, 0);
  const points: DashboardTrendPoint[] = [];

  while (cursor <= end) {
    const nextDay = new Date(cursor.getTime());
    nextDay.setDate(nextDay.getDate() + 1);
    const bucketStart = Math.max(cursor.getTime(), from.getTime());
    const bucketEnd = Math.min(nextDay.getTime(), to.getTime() + 1);
    const coverage =
      Math.max(0, bucketEnd - bucketStart) /
      (nextDay.getTime() - cursor.getTime());
    const weekdayWeight = [0.76, 1.03, 1.12, 1.08, 1.2, 0.93, 0.67][
      cursor.getDay()
    ];
    const orders = Math.max(
      1,
      Math.round((48 + random() * 110 * weekdayWeight) * coverage),
    );
    const codeOrders = Math.max(1, Math.round(orders * (0.58 + random() * 0.16)));
    const receivedCodes = Math.max(
      1,
      Math.round(codeOrders * (0.8 + random() * 0.15)),
    );
    points.push({
      averageCodeReceiptSeconds: randomInt(random, 22, 78),
      codeOrders,
      label: trendDateLabel(cursor, from, to),
      orders,
      purchaseOrders: Math.max(0, orders - codeOrders),
      receivedCodes,
      spend: roundMoney(orders * (0.7 + random() * 1.4)),
    });
    cursor.setDate(cursor.getDate() + 1);
  }

  return points;
}

function distributeRankingMetrics(
  random: Random,
  names: readonly string[],
  totalSpend: number,
  totalCodes: number,
  totalOrders: number,
  currentUserName?: string,
): DashboardRankItem[] {
  const weights = names.map((_, index) =>
    Math.max(0.6, names.length - index * 0.72 + random() * 0.9),
  );
  const counts = distributeWholeNumber(totalCodes, weights);
  const orders = distributeWholeNumber(totalOrders, weights);
  const spendInCents = distributeWholeNumber(Math.round(totalSpend * 100), weights);

  const items = names.map((name, index) => ({
    amount: spendInCents[index]! / 100,
    count: counts[index]!,
    ...(currentUserName ? { isCurrentUser: name === currentUserName } : {}),
    name,
    orders: orders[index]!,
  }));

  return items
    .sort((left, right) => right.count - left.count)
    .map((item, index) => ({ ...item, rank: index + 1 }));
}

function buildUserRanking(
  random: Random,
  currentUserName: string,
  totalSpend: number,
  totalCodes: number,
  totalOrders: number,
) {
  const leaderRatio = 0.72;
  const leaderSpend = roundMoney(totalSpend * leaderRatio);
  const leaderCodes = Math.floor(totalCodes * leaderRatio);
  const leaderOrders = Math.floor(totalOrders * leaderRatio);
  const leaders = distributeRankingMetrics(
    random,
    MOCK_USER_NAMES,
    leaderSpend,
    leaderCodes,
    leaderOrders,
    currentUserName,
  );
  const currentLeader = leaders.find((item) => item.isCurrentUser);

  if (currentLeader) {
    return { currentUserRank: currentLeader, leaders };
  }

  const rank = randomInt(random, 40, 120);
  const remainingCodes = Math.max(0, totalCodes - leaderCodes);
  const lowestLeaderCount = leaders[leaders.length - 1]?.count ?? 0;
  const estimatedCount = totalCodes > 0 ? Math.max(1, Math.floor(totalCodes / rank)) : 0;
  const count = Math.min(remainingCodes, lowestLeaderCount, estimatedCount);
  const share = totalCodes ? count / totalCodes : 0;

  return {
    currentUserRank: {
      amount: roundMoney(totalSpend * share),
      count,
      isCurrentUser: true,
      name: currentUserName,
      orders: Math.min(totalOrders, Math.max(count, Math.round(totalOrders * share))),
      rank,
    },
    leaders,
  };
}

function distributeWholeNumber(total: number, weights: number[]) {
  const safeTotal = Math.max(0, Math.round(total));
  const weightSum = weights.reduce((sum, weight) => sum + weight, 0) || 1;
  const rawValues = weights.map((weight) => (safeTotal * weight) / weightSum);
  const values = rawValues.map(Math.floor);
  let remainder = safeTotal - values.reduce((sum, value) => sum + value, 0);

  const remainderOrder = rawValues
    .map((value, index) => ({ fraction: value - Math.floor(value), index }))
    .sort((left, right) => right.fraction - left.fraction || left.index - right.index);

  for (let index = 0; index < remainder; index += 1) {
    values[remainderOrder[index % remainderOrder.length]!.index]! += 1;
  }

  return values;
}

function buildProjectSeries(
  random: Random,
  ranks: DashboardRankItem[],
  trend: DashboardTrendPoint[],
): DashboardProjectSeries[] {
  const featured = ranks.slice(0, 6);
  const totalRankWeight = featured.reduce((sum, item) => sum + item.count, 0) || 1;

  return featured.map((project) => {
    const projectWeight = project.count / totalRankWeight;
    return {
      name: project.name,
      receivedCodes: trend.map((point) =>
        Math.max(0, Math.round(point.receivedCodes * projectWeight * (0.8 + random() * 0.36))),
      ),
      spend: trend.map((point) =>
        roundMoney(point.spend * projectWeight * (0.8 + random() * 0.36)),
      ),
    };
  });
}

function sumTrend(points: DashboardTrendPoint[], field: keyof DashboardTrendPoint) {
  return points.reduce((sum, point) => {
    const value = point[field];
    return sum + (typeof value === "number" ? value : 0);
  }, 0);
}

function averageReceiptSeconds(points: DashboardTrendPoint[]) {
  const receivedCodes = sumTrend(points, "receivedCodes");
  if (!receivedCodes) return 0;

  const weightedSeconds = points.reduce(
    (sum, point) => sum + point.averageCodeReceiptSeconds * point.receivedCodes,
    0,
  );
  return Math.round(weightedSeconds / receivedCodes);
}

function simulateLatency(ms = 180) {
  return new Promise<void>((resolve) => globalThis.setTimeout(resolve, ms));
}

function parseDate(value?: string) {
  if (!value) return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed;
}

function normalizeRange(range: { from?: string; to?: string }, now: Date) {
  const parsedFrom = parseDate(range.from);
  const parsedTo = parseDate(range.to);
  const fallback = parsedFrom ?? parsedTo ?? now;
  let from = new Date((parsedFrom ?? fallback).getTime());
  let to = new Date((parsedTo ?? fallback).getTime());

  if (from.getTime() > to.getTime()) [from, to] = [to, from];
  if (to.getTime() > now.getTime()) to = new Date(now.getTime());
  if (from.getTime() > to.getTime()) from = new Date(to.getTime());

  const earliestAllowed = new Date(to.getTime());
  earliestAllowed.setDate(earliestAllowed.getDate() - (MAX_RANGE_DAYS - 1));
  if (from.getTime() < earliestAllowed.getTime()) from = earliestAllowed;

  return { from, to };
}

export async function getDashboardData(
  range: { from?: string; to?: string; username?: string } = {},
): Promise<DashboardData> {
  await simulateLatency();

  const now = new Date();
  now.setSeconds(0, 0);
  const { from, to } = normalizeRange(range, now);

  const random = createRandom(hashRange(from, to));
  const isSingleDay = sameDay(from, to);
  const trend = isSingleDay
    ? buildHourlyTrend(random, from, to)
    : buildDailyTrend(from, to, random);
  const totalSpend = roundMoney(sumTrend(trend, "spend"));
  const totalOrders = sumTrend(trend, "orders");
  const totalCodes = sumTrend(trend, "receivedCodes");
  const todayFrom = new Date(now.getTime());
  todayFrom.setHours(0, 0, 0, 0);
  const todaySeedEnd = new Date(todayFrom.getTime());
  todaySeedEnd.setHours(23, 59, 59, 999);
  const todayRandom = createRandom(hashRange(todayFrom, todaySeedEnd));
  const todayTrend = buildHourlyTrend(todayRandom, todayFrom, now);
  const todaySpend = roundMoney(sumTrend(todayTrend, "spend"));
  const todayOrders = sumTrend(todayTrend, "orders");
  const todayCodes = sumTrend(todayTrend, "receivedCodes");
  const currentUserName = range.username?.trim() || "donnel";
  const projectCodeRanking = distributeRankingMetrics(
    random,
    PROJECT_NAMES,
    totalSpend,
    totalCodes,
    totalOrders,
  );
  const projectSeries = buildProjectSeries(random, projectCodeRanking, trend);
  const historicalRandom = createRandom(0x6d7a_21f3);
  const historicalSpend = roundMoney(12680 + historicalRandom() * 2400);
  const historicalCodes = 48_000 + randomInt(historicalRandom, 0, 8_000);
  const historicalOrders = 65_000 + randomInt(historicalRandom, 0, 12_000);
  const historicalUserRanking = buildUserRanking(
    historicalRandom,
    currentUserName,
    historicalSpend,
    historicalCodes,
    historicalOrders,
  );
  const todayUserRanking = buildUserRanking(
    todayRandom,
    currentUserName,
    todaySpend,
    todayCodes,
    todayOrders,
  );

  const codeOrders = sumTrend(trend, "codeOrders");
  const codeRatio = totalOrders ? Math.round((codeOrders / totalOrders) * 100) : 0;

  return {
    codeRatio,
    historicalCodeRanking: historicalUserRanking.leaders,
    historicalCurrentUserRank: historicalUserRanking.currentUserRank,
    projectCodeRanking,
    projectSeries,
    purchaseRatio: totalOrders ? 100 - codeRatio : 0,
    stats: {
      averageCodeReceiptSeconds: averageReceiptSeconds(trend),
      codeSuccessRate: codeOrders
        ? Number(((totalCodes / codeOrders) * 100).toFixed(1))
        : 0,
      historicalSpend,
      todayCodeReceipts: todayCodes,
      todayOrders,
      totalCodeReceipts: totalCodes,
      totalOrders,
      walletBalance: roundMoney(640 + historicalRandom() * 960),
    },
    todayCodeRanking: todayUserRanking.leaders,
    todayCurrentUserRank: todayUserRanking.currentUserRank,
    trend,
  };
}
