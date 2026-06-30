import createClient from "openapi-fetch";
import type { components, operations, paths } from "./openapi/schema";

type JsonResponse<
  Operation extends { responses: Record<number, unknown> },
  Status extends keyof Operation["responses"],
> = Operation["responses"][Status] extends {
  content: { "application/json": infer Body };
}
  ? Body
  : never;

export type ApiErrorBody = Partial<components["schemas"]["Error"]>;
export type UserResponse = components["schemas"]["UserResponse"];
export type ActivationResponse = components["schemas"]["ActivationResponse"];
export type CaptchaResponse = components["schemas"]["CaptchaResponse"];
export type ActivationRequest = components["schemas"]["ActivationRequest"];
export type LoginRequest = components["schemas"]["LoginRequest"];
export type EmailCodeRequest = components["schemas"]["EmailCodeRequest"];
export type RegisterRequest = components["schemas"]["RegisterRequest"];
export type ChangePasswordRequest = components["schemas"]["ChangePasswordRequest"];
export type PasswordResetCodeRequest =
  components["schemas"]["PasswordResetCodeRequest"];
export type PasswordResetRequest = components["schemas"]["PasswordResetRequest"];
export type ActivationUserResponse = JsonResponse<
  operations["postActivation"],
  201
>;
export type LoginResponse = components["schemas"]["LoginResponse"];
export type RegisterResponse = JsonResponse<operations["postRegister"], 201>;
export type MeResponse = JsonResponse<operations["getMe"], 200>;

const csrfCookieName = "csrf_token";
const csrfHeaderName = "X-CSRF-Token";

const client = createClient<paths>({
  baseUrl: "",
  credentials: "include",
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

async function unwrap<T>(result: ApiResult<T>): Promise<T> {
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

function csrfHeader() {
  return {
    [csrfHeaderName]: readCookie(csrfCookieName),
  };
}

export async function getActivation() {
  return unwrap<ActivationResponse>(await client.GET("/v1/activation"));
}

export async function activateSystem(payload: ActivationRequest) {
  return unwrap<ActivationUserResponse>(
    await client.POST("/v1/activation", { body: payload })
  );
}

export async function createCaptcha() {
  return unwrap<CaptchaResponse>(await client.POST("/v1/captchas"));
}

export async function sendEmailCode(payload: EmailCodeRequest) {
  return unwrap<void>(
    await client.POST("/v1/email/code", { body: payload })
  );
}

export async function login(payload: LoginRequest) {
  return unwrap<LoginResponse>(
    await client.POST("/v1/sessions", { body: payload })
  );
}

export async function logout() {
  return unwrap<void>(
    await client.DELETE("/v1/sessions/current", {
      params: { header: csrfHeader() },
    })
  );
}

export async function getMe() {
  return unwrap<MeResponse>(await client.GET("/v1/me"));
}

export async function registerUser(payload: RegisterRequest) {
  return unwrap<RegisterResponse>(
    await client.POST("/v1/users", { body: payload })
  );
}

export async function changePassword(payload: ChangePasswordRequest) {
  return unwrap<void>(
    await client.PATCH("/v1/password", {
      body: payload,
      params: { header: csrfHeader() },
    })
  );
}

export async function requestPasswordReset(payload: PasswordResetCodeRequest) {
  return unwrap<void>(
    await client.POST("/v1/password/reset/request", { body: payload })
  );
}

export async function resetPassword(payload: PasswordResetRequest) {
  return unwrap<void>(
    await client.POST("/v1/password/reset", { body: payload })
  );
}
