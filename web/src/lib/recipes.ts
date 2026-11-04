"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type RecipeSummary = components["schemas"]["RecipeSummary"];
export type RecipeVersion = components["schemas"]["RecipeVersion"];
export type RecipeVersionDetail = components["schemas"]["RecipeVersionDetail"];
export type RecipeCheck = components["schemas"]["RecipeCheck"];
export type RecipeParsed = components["schemas"]["RecipeParsed"];
export type RecipeExample = components["schemas"]["RecipeExample"];
export type RecipeStep = RecipeParsed["roles"][number]["steps"][number];

export function useRecipes() {
  return useQuery({ queryKey: ["recipes"], queryFn: async () => unwrap(await api.GET("/recipes")) });
}

export function useRecipeExamples() {
  return useQuery({
    queryKey: ["recipes", "examples"],
    queryFn: async () => unwrap(await api.GET("/recipes/examples")),
    staleTime: Infinity,
  });
}

export function useRecipeVersion(name: string | null, version: string | null) {
  return useQuery({
    queryKey: ["recipes", name, version],
    enabled: !!name && !!version,
    queryFn: async () =>
      unwrap(
        await api.GET("/recipes/{recipeName}/versions/{recipeVersion}", {
          params: { path: { recipeName: name!, recipeVersion: version! } },
        }),
      ),
  });
}

// useRecipeCheck controleert de tekst een halve seconde nadat je stopt met
// typen.
export function useRecipeCheck(source: string) {
  const [debounced, setDebounced] = useState(source);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(source), 500);
    return () => clearTimeout(t);
  }, [source]);
  return useQuery({
    queryKey: ["recipes", "check", debounced],
    enabled: debounced.trim() !== "",
    placeholderData: keepPreviousData,
    queryFn: async () => unwrap(await api.POST("/recipes/check", { body: { source: debounced } })),
  });
}

export function useSaveRecipe() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (source: string) => unwrap(await api.POST("/recipes", { body: { source } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["recipes"] }),
  });
}

export function useDeleteRecipeVersion() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ name, version }: { name: string; version: string }) =>
      unwrap(
        await api.DELETE("/recipes/{recipeName}/versions/{recipeVersion}", {
          params: { path: { recipeName: name, recipeVersion: version } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["recipes"] }),
  });
}

export const stepKinds: Record<string, string> = {
  package: "Pakketten",
  file: "Bestand",
  ini_set: "Instelling",
  service: "Dienst",
  user: "Gebruiker",
  directory: "Map",
  command: "Commando",
  repo_deb: "Repository",
  repo: "Repository",
  db_database: "Database",
  db_user: "Databasegebruiker",
  db_import: "Schema laden",
};

export function needLabel(n: RecipeParsed["needs"][number]) {
  if (n.service === "vip") return "VIP";
  if (n.service === "database") return n.engine === "postgresql" ? "PostgreSQL Patroni" : "MariaDB Galera";
  return n.service;
}

export const healthLabel = (h: RecipeParsed["roles"][number]["health"][number]) => {
  switch (h.kind) {
    case "service":
      return `dienst ${h.target} draait`;
    case "http":
      return `${h.target} geeft ${h.expect}`;
    case "port":
      return `poort ${h.target} luistert`;
    case "db":
      return "de database mag schrijven";
  }
  return h.kind;
};
