"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { api, ApiError, setCsrfToken, unwrap, type Me } from "./api/client";

export const meQueryKey = ["auth", "me"] as const;

export async function fetchMe(): Promise<Me | null> {
  const res = await api.GET("/auth/me");
  if (res.response.status === 401) {
    setCsrfToken(null);
    return null;
  }
  const me = unwrap(res);
  setCsrfToken(me.csrf_token);
  return me;
}

export function useMe() {
  return useQuery({ queryKey: meQueryKey, queryFn: fetchMe, staleTime: 60_000 });
}

// useRequireMe stuurt naar /login als er geen sessie is.
export function useRequireMe() {
  const router = useRouter();
  const q = useMe();
  useEffect(() => {
    if (q.isSuccess && q.data === null) {
      // Na het inloggen terug naar de pagina waar je was.
      const here = window.location.pathname + window.location.search;
      router.replace(here === "/" ? "/login" : `/login?terug=${encodeURIComponent(here)}`);
    }
  }, [q.isSuccess, q.data, router]);
  return q;
}

export function useLogout() {
  const qc = useQueryClient();
  const router = useRouter();
  return async () => {
    try {
      unwrap(await api.POST("/auth/logout"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) throw e;
    }
    setCsrfToken(null);
    qc.setQueryData(meQueryKey, null);
    router.replace("/login");
  };
}

// returnPath is de pagina om na het inloggen naar terug te keren: alleen een
// pad binnen ClusterForge, nooit een ander adres.
export function returnPath(search: string): string {
  const t = new URLSearchParams(search).get("terug") ?? "";
  return t.startsWith("/") && !t.startsWith("//") && !t.startsWith("/\\") && !t.startsWith("/login") ? t : "/";
}

export const prod2FAKey = ["security", "prod-2fa"] as const;

// useProd2FA zegt of acties op prod tweestapsverificatie vragen (een
// instelling van de installatie; standaard aan).
export function useProd2FA() {
  return useQuery({
    queryKey: prod2FAKey,
    queryFn: async () => unwrap(await api.GET("/security/prod-2fa")),
    staleTime: 60_000,
  });
}

// useProdAllowed is true als de gebruiker op prod mag handelen: met
// tweestapsverificatie, of als de installatie die niet vraagt.
export function useProdAllowed() {
  const me = useMe();
  const req = useProd2FA();
  if (me.data?.user.totp_enabled) return true;
  return req.data ? !req.data.required : false;
}
