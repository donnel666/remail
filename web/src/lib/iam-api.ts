import type { components, operations } from "./openapi/schema";
import {
  apiClient as client,
  csrfHeader,
  IamApiError,
  unwrap,
  type JsonResponse,
} from "./api-client";

export type { ApiErrorBody } from "./api-client";
export { IamApiError };
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
export type AdminUserListResponse =
  components["schemas"]["AdminUserListResponse"];
export type RegisterResponse = JsonResponse<operations["postRegister"], 201>;
export type MeResponse = JsonResponse<operations["getMe"], 200>;

export interface AdminUserListFilter {
  ids?: number[];
  limit?: number;
  offset?: number;
  search?: string;
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

export async function listAdminUsers(filter: AdminUserListFilter = {}) {
  return unwrap<AdminUserListResponse>(
    await client.GET("/v1/admin/users", {
      params: { query: filter },
    })
  );
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
