import { useEffect, useState } from "react";
import { toast } from "sonner";
import { TriangleAlert } from "lucide-react";
import { api } from "@/lib/api";
import { useAuth } from "@/auth";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { CodeBlock, PageHeader, Skeleton } from "@/components/ui/misc";
import { Switch } from "@/components/ui/switch";

interface S {
  open_signup: boolean;
  allow_anonymous: boolean;
}

export function Settings() {
  const { me, refresh } = useAuth();
  const [s, setS] = useState<S | null>(null);
  useEffect(() => {
    api<S>("/api/admin/settings").then(setS).catch((e) => toast.error(e.message));
  }, []);

  async function set(patch: Partial<S>) {
    const prev = s;
    setS((x) => (x ? { ...x, ...patch } : x));
    try {
      setS(await api<S>("/api/admin/settings", { method: "PUT", body: patch }));
      await refresh();
    } catch (e) {
      setS(prev);
      toast.error((e as Error).message);
    }
  }

  return (
    <>
      <PageHeader title="Settings" description="Instance-wide switches. Only admins see this page." />
      {!s && <Skeleton className="h-48 w-full" />}
      {s && (
        <div className="grid gap-4">
          <Card>
            <CardHeader className="flex-row items-start justify-between gap-4">
              <div className="grid gap-1.5">
                <CardTitle>Open sign-up</CardTitle>
                <CardDescription>
                  Off: only people you add under Users can sign in. On: anyone who can log in with your provider gets an account
                  automatically.
                </CardDescription>
              </div>
              <Switch checked={s.open_signup} onCheckedChange={(v) => set({ open_signup: v })} aria-label="Open sign-up" />
            </CardHeader>
          </Card>
          <Card>
            <CardHeader className="flex-row items-start justify-between gap-4">
              <div className="grid gap-1.5">
                <CardTitle>Public endpoint (no auth)</CardTitle>
                <CardDescription>
                  Turns on a second URL that needs no sign-in. It exposes only MCPs whose owners ticked “Public”. The authenticated gateway
                  URL is unaffected.
                </CardDescription>
              </div>
              <Switch checked={s.allow_anonymous} onCheckedChange={(v) => set({ allow_anonymous: v })} aria-label="Public endpoint" />
            </CardHeader>
            {s.allow_anonymous && me && (
              <CardContent className="grid gap-3">
                <p className="flex items-start gap-2 text-sm text-warning">
                  <TriangleAlert className="mt-0.5 size-4 shrink-0" />
                  Anyone on the internet can call public MCPs using the owner's stored credentials for them.
                </p>
                <CodeBlock code={me.public_mcp_url} />
              </CardContent>
            )}
          </Card>
        </div>
      )}
    </>
  );
}
