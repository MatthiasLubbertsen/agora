import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ShieldCheck } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { Logo } from "@/components/layout";

interface Info {
  client_name?: string;
  redirect_host?: string;
  user?: string;
  redirect?: string;
}

export function Consent() {
  const [params] = useSearchParams();
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<"" | "yes" | "no">("");

  useEffect(() => {
    api<Info>("/api/oauth/authorize?" + params.toString())
      .then((i) => (i.redirect ? (window.location.href = i.redirect) : setInfo(i)))
      .catch((e) => setError(e.message));
  }, [params]);

  async function decide(approve: boolean) {
    setBusy(approve ? "yes" : "no");
    try {
      const res = await api<{ redirect: string }>("/api/oauth/authorize", {
        method: "POST",
        body: { approve, params: Object.fromEntries(params.entries()) },
      });
      window.location.href = res.redirect;
    } catch (e) {
      setError((e as Error).message);
      setBusy("");
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex justify-center">
          <Logo className="size-9" />
        </div>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <ShieldCheck className="size-5" /> Authorise access
            </CardTitle>
            {info && (
              <CardDescription>
                <span className="font-medium text-foreground">{info.client_name}</span> wants to use your MCPs through the gateway.
              </CardDescription>
            )}
          </CardHeader>
          <CardContent className="grid gap-4">
            {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
            {!info && !error && <Skeleton className="h-16 w-full" />}
            {info && (
              <>
                <dl className="grid gap-2 rounded-md border bg-muted/40 p-3 text-sm">
                  <div className="flex justify-between gap-4"><dt className="text-muted-foreground">Signed in as</dt><dd className="truncate">{info.user}</dd></div>
                  <div className="flex justify-between gap-4"><dt className="text-muted-foreground">Returns to</dt><dd className="truncate font-mono text-[13px]">{info.redirect_host}</dd></div>
                </dl>
                <p className="text-xs text-muted-foreground">
                  It will be able to list and call the tools of every MCP you've added. You can revoke it any time under Access.
                </p>
                <div className="grid grid-cols-2 gap-2">
                  <Button variant="outline" onClick={() => decide(false)} loading={busy === "no"} disabled={!!busy}>Deny</Button>
                  <Button onClick={() => decide(true)} loading={busy === "yes"} disabled={!!busy}>Allow</Button>
                </div>
              </>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
