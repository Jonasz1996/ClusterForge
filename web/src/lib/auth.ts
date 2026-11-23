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

// setupPending: na een reset via de CLI moet de gebruiker eerst een nieuw
// wachtwoord en een nieuwe TOTP instellen; dat gebeurt op /login.
export function setupPending(me: Me): boolean {
  return me.user.must_change_password || me.user.must_setup_totp;
}

// useRequireMe stuurt naar /login als er geen sessie is, of als het account
// na een reset nog ingesteld moet worden.
export function useRequireMe() {
  const router = useRouter();
  const q = useMe();
  useEffect(() => {
    if (q.isSuccess && q.data === null) {
      // Na het inloggen terug naar de pagina waar je was.
      const here = window.location.pathname + window.location.search;
      router.replace(here === "/" ? "/login" : `/login?terug=${encodeURIComponent(here)}`);
    } else if (q.data && setupPending(q.data)) {
      router.replace("/login");
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
