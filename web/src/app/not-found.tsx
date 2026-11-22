import Link from "next/link";

export default function NotFound() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-3 px-4 text-center">
      <title>Niet gevonden · ClusterForge</title>
      <h1 className="text-2xl font-semibold">Pagina niet gevonden</h1>
      <Link href="/" className="text-sm font-medium text-brand-600 underline">
        Terug naar het overzicht
      </Link>
    </main>
  );
}
