import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Me = components["schemas"]["Me"];
export type User = components["schemas"]["User"];
export type ApiEvent = components["schemas"]["Event"];
export type ApiErrorBody = components["schemas"]["Error"];

// De CSRF-token komt uit de login-respons of /auth/me en gaat mee op elke
// request die iets wijzigt.
let csrfToken: string | null = null;

export function setCsrfToken(token: string | null) {
  csrfToken = token;
}

const csrfMiddleware: Middleware = {
  onRequest({ request }) {
    if (csrfToken && !["GET", "HEAD", "OPTIONS"].includes(request.method)) {
      request.headers.set("X-CSRF-Token", csrfToken);
    }
    return request;
  },
};

export const api = createClient<paths>({ baseUrl: "/api/v1", credentials: "same-origin" });
api.use(csrfMiddleware);

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

// unwrap geeft de data terug of gooit een ApiError met de foutcode van de server.
export function unwrap<T>(res: { data?: T; error?: unknown; response: Response }): T {
  if (res.error !== undefined || !res.response.ok) {
    const body = res.error as Partial<ApiErrorBody> | undefined;
    throw new ApiError(
      res.response.status,
      body?.code ?? "unknown",
      body?.message ?? `Onverwachte fout (${res.response.status})`,
    );
  }
  return res.data as T;
}
