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
export type TurnstileConfigResponse =
  components["schemas"]["TurnstileConfigResponse"];
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
export type LoginConfigResponse = components["schemas"]["LoginConfigResponse"];
export type LinuxDOAccountMode = components["schemas"]["LinuxDOAccountMode"];
export type LinuxDOPendingResponse = components["schemas"]["LinuxDOPendingResponse"];
export type LinuxDOEmailCodeRequest = components["schemas"]["LinuxDOEmailCodeRequest"];
export type LinuxDOCompleteRequest = components["schemas"]["LinuxDOCompleteRequest"];
export type GitHubPendingResponse = components["schemas"]["GitHubPendingResponse"];
export type GitHubEmailCodeRequest = components["schemas"]["GitHubEmailCodeRequest"];
export type GitHubCompleteRequest = components["schemas"]["GitHubCompleteRequest"];
export type NodeLocAccountMode = LinuxDOAccountMode;
export type NodeLocPendingResponse = components["schemas"]["NodeLocPendingResponse"];
export type NodeLocEmailCodeRequest = LinuxDOEmailCodeRequest;
export type NodeLocCompleteRequest = LinuxDOCompleteRequest;
export type AdminUserListResponse =
  components["schemas"]["AdminUserListResponse"];
export type CurrentInviteResponse =
  components["schemas"]["CurrentInviteResponse"];
export type UserGroupListResponse =
  components["schemas"]["AdminUserGroupListResponse"];
export type RegisterResponse = JsonResponse<operations["postRegister"], 201>;
export type MeResponse = JsonResponse<operations["getMe"], 200>;

export interface AdminUserListFilter {
  ids?: number[];
  limit?: number;
  offset?: number;
  search?: string;
}

export async function getActivation() {
  if (import.meta.env.DEV) {
    return (await import("./dev-api-mocks")).DEV_ACTIVATION as ActivationResponse;
  }
  return unwrap<ActivationResponse>(await client.GET("/v1/activation"));
}

export async function activateSystem(payload: ActivationRequest) {
  return unwrap<ActivationUserResponse>(
    await client.POST("/v1/activation", { body: payload })
  );
}

export async function getTurnstileConfig() {
  return unwrap<TurnstileConfigResponse>(
    await client.GET("/v1/turnstile/config")
  );
}

export async function sendEmailCode(payload: EmailCodeRequest) {
  const result = await client.POST("/v1/email/code", { body: payload });
  await unwrap<void>(result);
  return Number.parseInt(result.response.headers.get("Retry-After") ?? "", 10) || 0;
}

export async function login(payload: LoginRequest) {
  return unwrap<LoginResponse>(
    await client.POST("/v1/login", { body: payload })
  );
}

export async function getLoginConfig() {
  return unwrap<LoginConfigResponse>(await client.GET("/v1/login/config"));
}

export const linuxDOLoginURL = "/v1/oauth/linuxdo";
export const linuxDOBindURL = "/v1/oauth/linuxdo/bind";
export const githubLoginURL = "/v1/oauth/github";
export const githubBindURL = "/v1/oauth/github/bind";
export const nodeLocLoginURL = "/v1/oauth/nodeloc";
export const nodeLocBindURL = "/v1/oauth/nodeloc/bind";

export async function getLinuxDOPending() {
  return unwrap<LinuxDOPendingResponse>(
    await client.GET("/v1/oauth/linuxdo/pending")
  );
}

export async function sendLinuxDOEmailCode(payload: LinuxDOEmailCodeRequest) {
  const result = await client.POST("/v1/oauth/linuxdo/email/code", {
    body: payload,
  });
  await unwrap<void>(result);
  return Number.parseInt(result.response.headers.get("Retry-After") ?? "", 10) || 0;
}

export async function completeLinuxDO(payload: LinuxDOCompleteRequest) {
  return unwrap<LoginResponse>(
    await client.POST("/v1/oauth/linuxdo/complete", { body: payload })
  );
}

export async function getGitHubPending() {
  return unwrap<GitHubPendingResponse>(
    await client.GET("/v1/oauth/github/pending")
  );
}

export async function sendGitHubEmailCode(payload: GitHubEmailCodeRequest) {
  const result = await client.POST("/v1/oauth/github/email/code", {
    body: payload,
  });
  await unwrap<void>(result);
  return Number.parseInt(result.response.headers.get("Retry-After") ?? "", 10) || 0;
}

export async function completeGitHub(payload: GitHubCompleteRequest) {
  return unwrap<LoginResponse | undefined>(
    await client.POST("/v1/oauth/github/complete", { body: payload })
  );
}

export async function getNodeLocPending() {
  return unwrap<NodeLocPendingResponse>(
    await client.GET("/v1/oauth/nodeloc/pending")
  );
}

export async function sendNodeLocEmailCode(payload: NodeLocEmailCodeRequest) {
  const result = await client.POST("/v1/oauth/nodeloc/email/code", {
    body: payload,
  });
  await unwrap<void>(result);
  return Number.parseInt(result.response.headers.get("Retry-After") ?? "", 10) || 0;
}

export async function completeNodeLoc(payload: NodeLocCompleteRequest) {
  return unwrap<LoginResponse>(
    await client.POST("/v1/oauth/nodeloc/complete", { body: payload })
  );
}

export async function logout() {
  return unwrap<void>(await client.DELETE("/v1/sessions/current"));
}

export async function getMe() {
  if (import.meta.env.DEV) {
    return (await import("./dev-api-mocks")).DEV_ME as MeResponse;
  }
  return unwrap<MeResponse>(await client.GET("/v1/me"));
}

export async function getUserGroups() {
  if (import.meta.env.DEV) {
    return (await import("./dev-api-mocks"))
      .DEV_USER_GROUPS as UserGroupListResponse;
  }
  return unwrap<UserGroupListResponse>(await client.GET("/v1/user-groups"));
}

export async function getMyInvite() {
  return unwrap<CurrentInviteResponse>(await client.GET("/v1/me/invite"));
}

export async function createMyInvite() {
  return unwrap<CurrentInviteResponse>(
    await client.POST("/v1/me/invite", {
      params: { header: csrfHeader() },
    })
  );
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
  const result = await client.POST("/v1/password/reset/request", { body: payload });
  await unwrap<void>(result);
  return Number.parseInt(result.response.headers.get("Retry-After") ?? "", 10) || 0;
}

export async function resetPassword(payload: PasswordResetRequest) {
  return unwrap<void>(
    await client.POST("/v1/password/reset", { body: payload })
  );
}
