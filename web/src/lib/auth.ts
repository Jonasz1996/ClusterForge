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
    if (q.isSuccess && q.data === null) router.replace("/login");
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
