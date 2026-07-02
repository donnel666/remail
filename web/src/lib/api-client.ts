import createClient from "openapi-fetch";
import type { components, paths } from "./openapi/schema";

export type JsonResponse<
  Operation extends { responses: Record<number, unknown> },
  Status extends keyof Operation["responses"],
> = Operation["responses"][Status] extends {
  content: { "application/json": infer Body };
}
  ? Body
  : never;

export type ApiErrorBody = Partial<components["schemas"]["Error"]>;

const csrfCookieName = "csrf_token";
const csrfHeaderName = "X-CSRF-Token";
export const apiRequestTimeoutMs = 600_000;

function withTimeoutSignal(input: Request, timeoutMs: number) {
  const controller = new AbortController();
  const timeoutID = globalThis.setTimeout(() => controller.abort(), timeoutMs);
  const parentSignal = input.signal;
  const abortFromParent = () => controller.abort();

  if (parentSignal.aborted) {
    abortFromParent();
  } else {
    parentSignal.addEventListener("abort", abortFromParent, { once: true });
  }

  return {
    request: new Request(input, { signal: controller.signal }),
    cleanup: () => {
      globalThis.clearTimeout(timeoutID);
      parentSignal.removeEventListener("abort", abortFromParent);
    },
  };
}

async function fetchWithTimeout(input: Request) {
  const { request, cleanup } = withTimeoutSignal(input, apiRequestTimeoutMs);
  try {
    return await globalThis.fetch(request);
  } finally {
    cleanup();
  }
}

export const apiClient = createClient<paths>({
  baseUrl: "",
  credentials: "include",
  fetch: fetchWithTimeout,
  headers: {
    Accept: "application/json",
  },
});

export class IamApiError extends Error {
  readonly status: number;
  readonly requestId?: string;
  readonly fields?: ApiErrorBody["fields"];

  constructor(status: number, body: ApiErrorBody) {
    super(body.message || "Request failed.");
    this.name = "IamApiError";
    this.status = status;
    this.requestId = body.requestId;
    this.fields = body.fields;
  }
}

interface ApiResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

function normalizeErrorBody(error: unknown): ApiErrorBody {
  if (!error) return {};
  if (typeof error === "string") return { message: error };
  if (typeof error === "object") return error as ApiErrorBody;
  return { message: String(error) };
}

export async function unwrap<T>(result: ApiResult<T>): Promise<T> {
  if (!result.response.ok) {
    throw new IamApiError(
      result.response.status,
      normalizeErrorBody(result.error)
    );
  }
  return result.data as T;
}

function readCookie(name: string) {
  if (typeof document === "undefined") return "";

  const prefix = `${name}=`;
  const value = document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(prefix))
    ?.slice(prefix.length);

  if (!value) return "";

  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function csrfHeader() {
  return {
    [csrfHeaderName]: readCookie(csrfCookieName),
  };
}
