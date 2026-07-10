import { SharedArray } from "k6/data";
import http from "k6/http";
import { check } from "k6";
import { Counter, Rate } from "k6/metrics";

const baseURL = __ENV.BASE_URL || "http://127.0.0.1:8080";
const multiplier = Number(__ENV.MULTIPLIER || "1");
const orderRate = Number(__ENV.ORDER_RATE || "300") * multiplier;
const pickupRate = Number(__ENV.PICKUP_RATE || "3000") * multiplier;
const duration = __ENV.DURATION || (multiplier > 1 ? "5m" : "15m");
const apiKeys = (__ENV.API_KEYS || __ENV.API_KEY || "")
  .split(",")
  .map((value) => value.trim())
  .filter(Boolean);
const projectId = Number(__ENV.PROJECT_ID || "0");
const productId = Number(__ENV.PRODUCT_ID || "0");
const pickupFixturePath = __ENV.PICKUP_FIXTURES || "./pickup-fixtures.json";

const pickupFixtures = new SharedArray("pickup fixtures", () => {
  try {
    return JSON.parse(open(pickupFixturePath));
  } catch {
    return [];
  }
});

if (apiKeys.length === 0 || projectId <= 0 || productId <= 0) {
  throw new Error("API_KEYS (or API_KEY), PROJECT_ID, and PRODUCT_ID are required");
}
if (pickupFixtures.length === 0) {
  throw new Error("PICKUP_FIXTURES must contain at least one email/token pair");
}

const checkoutRequests = new Counter("remail_checkout_requests");
const pickupRequests = new Counter("remail_pickup_requests");
const unexpectedFailures = new Rate("remail_unexpected_failures");

export const options = {
  discardResponseBodies: true,
  scenarios: {
    checkout: {
      executor: "constant-arrival-rate",
      exec: "checkout",
      rate: orderRate,
      timeUnit: "1s",
      duration,
      preAllocatedVUs: Math.max(100, Math.ceil(orderRate / 2)),
      maxVUs: Math.max(500, orderRate * 2),
      tags: { workload: "checkout" },
    },
    pickup: {
      executor: "constant-arrival-rate",
      exec: "pickup",
      rate: pickupRate,
      timeUnit: "1s",
      duration,
      preAllocatedVUs: Math.max(500, Math.ceil(pickupRate / 2)),
      maxVUs: Math.max(3000, pickupRate * 2),
      tags: { workload: "pickup" },
    },
  },
  thresholds: {
    "http_req_duration{workload:checkout}": ["p(95)<300", "p(99)<800"],
    "http_req_duration{workload:pickup}": ["p(95)<50", "p(99)<100"],
    remail_checkout_requests: ["count>0"],
    remail_pickup_requests: ["count>0"],
    remail_unexpected_failures: ["rate<0.001"],
  },
};

http.setResponseCallback(http.expectedStatuses(200, 201, 422, 429));

export function checkout() {
  const apiKey = apiKeys[__VU % apiKeys.length];
  const idempotencyKey = `k6-${__VU}-${__ITER}-${Date.now()}`;
  const response = http.post(
    `${baseURL}/v1/open/orders?serviceMode=code&supply=public_only`,
    JSON.stringify({ projectId, productId }),
    {
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json",
        "Idempotency-Key": idempotencyKey,
      },
      tags: { workload: "checkout" },
    }
  );
  checkoutRequests.add(1);
  const accepted = [201, 422, 429].includes(response.status);
  unexpectedFailures.add(!accepted);
  check(response, {
    "checkout accepted": () => accepted,
  });
}

export function pickup() {
  const fixture = pickupFixtures[__ITER % pickupFixtures.length];
  const response = http.get(
    `${baseURL}/v1/pickup?email=${encodeURIComponent(fixture.email)}&token=${encodeURIComponent(fixture.token)}`,
    { tags: { workload: "pickup" } }
  );
  pickupRequests.add(1);
  const accepted = [200, 429].includes(response.status);
  unexpectedFailures.add(!accepted);
  check(response, {
    "pickup accepted": () => accepted,
  });
}
