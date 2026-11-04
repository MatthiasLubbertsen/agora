import { useEffect, useState } from "react";
import { Navigate, useSearchParams } from "react-router-dom";
import { KeyRound } from "lucide-react";
import { api } from "@/lib/api";
import { useAuth } from "@/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { Logo } from "@/components/layout";

interface Providers {
  providers: { id: string; label: string }[];
  needs_setup: boolean;
}

function useProviders() {
  const [data, setData] = useState<Providers | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    api<Providers>("/api/auth/providers").then(setData).catch(() => setFailed(true));
  }, []);
  return { data, failed };
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <Logo className="size-9" />
          <h1 className="text-xl font-semibold tracking-tight">MCP Full Of MCPs</h1>
        </div>
        {children}
      </div>
    </div>
  );
}

function ProviderButtons({ providers, extra }: { providers: Providers["providers"]; extra?: string }) {
  const [params] = useSearchParams();
  const next = params.get("next") ?? "/";
  return (
    <div className="grid gap-2">
      {providers.map((p) => (
        <Button key={p.id} variant="outline" size="lg" asChild>
          <a href={`/auth/login/${p.id}?next=${encodeURIComponent(next)}${extra ?? ""}`}>
            <KeyRound /> Continue with {p.label}
          </a>
        </Button>
      ))}
    </div>
  );
}

export function Login() {
  const { me, loading } = useAuth();
  const { data, failed } = useProviders();
  const [params] = useSearchParams();
  const error = params.get("error");
  if (!loading && me) return <Navigate to={params.get("next") || "/"} replace />;
  return (
    <Shell>
      <Card>
        <CardHeader>
          <CardTitle>Sign in</CardTitle>
          <CardDescription>Use your organisation's login to continue.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {error && (
            <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {error}
            </p>
          )}
          {failed && <p className="text-sm text-destructive">Couldn't reach the server.</p>}
          {!data && !failed && <Skeleton className="h-10 w-full" />}
          {data && data.providers.length > 0 && <ProviderButtons providers={data.providers} />}
          {data && data.providers.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No login provider is configured. Set <code className="text-foreground">OIDC_PROVIDERS</code> on the server.
            </p>
          )}
          {data?.needs_setup && (
            <p className="text-xs text-muted-foreground">
              First time here? Open the setup link printed in the server's startup log to create the admin account.
            </p>
          )}
        </CardContent>
      </Card>
    </Shell>
  );
}

export function Setup() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const { data } = useProviders();
  const error = params.get("error");
  return (
    <Shell>
      <Card>
        <CardHeader>
          <CardTitle>Create the admin account</CardTitle>
          <CardDescription>
            Sign in with the account that should administer this gateway. This link works once, for the first admin only.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {error && <p className="text-sm text-destructive">{error}</p>}
          {!token && <p className="text-sm text-destructive">This link is missing its token. Use the full link from the server log.</p>}
          {!data && <Skeleton className="h-10 w-full" />}
          {data && token && !data.needs_setup && (
            <p className="text-sm text-muted-foreground">An admin already exists. <a className="underline" href="/login">Go to sign in</a>.</p>
          )}
          {data && token && data.needs_setup && <ProviderButtons providers={data.providers} extra={`&setup=${encodeURIComponent(token)}`} />}
        </CardContent>
      </Card>
    </Shell>
  );
}
