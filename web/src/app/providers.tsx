"use client";

import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { ApiError } from "@/lib/api/client";
import { meQueryKey } from "@/lib/auth";

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(() => {
    // Een 401 midden in het werk betekent dat de sessie verlopen of beëindigd
    // is: dan meteen naar de loginpagina, in plaats van foutmeldingen op elke
    // kaart.
    const onError = (err: unknown) => {
      if (err instanceof ApiError && err.status === 401 && err.code === "unauthenticated" && c.getQueryData(meQueryKey)) {
        c.setQueryData(meQueryKey, null);
      }
    };
    const c: QueryClient = new QueryClient({
        queryCache: new QueryCache({ onError }),
        mutationCache: new MutationCache({ onError }),
        defaultOptions: {
          queries: {
            retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
            refetchOnWindowFocus: true,
          },
        },
      });
    return c;
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
