import { useEffect, useState } from "react";
import { toast } from "sonner";
import { AppWindow, KeyRound, Plus, Trash2 } from "lucide-react";
import { api, type Grant, type Token, type Upstream } from "@/lib/api";
import { useAuth } from "@/auth";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { CodeBlock, EmptyState, PageHeader, Skeleton } from "@/components/ui/misc";
import { timeAgo } from "@/lib/utils";

export function Tokens() {
  const { me } = useAuth();
  const [tokens, setTokens] = useState<Token[] | null>(null);
  const [grants, setGrants] = useState<Grant[] | null>(null);
  const [ups, setUps] = useState<Upstream[]>([]);
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<string | null>(null);

  const load = () => {
    api<Token[]>("/api/tokens").then(setTokens).catch((e) => toast.error(e.message));
    api<Grant[]>("/api/grants").then(setGrants).catch(() => setGrants([]));
  };
  useEffect(() => {
    load();
    api<Upstream[]>("/api/upstreams").then(setUps).catch(() => {});
  }, []);

  const names = Object.fromEntries(ups.map((u) => [u.id, u.name]));

  async function revoke(t: Token) {
    await api(`/api/tokens/${t.id}`, { method: "DELETE" }).catch((e) => toast.error(e.message));
    toast.success(`Revoked ${t.name}`);
    load();
  }
  async function revokeGrant(g: Grant) {
    await api(`/api/grants/${encodeURIComponent(g.client_id)}`, { method: "DELETE" }).catch((e) => toast.error(e.message));
    toast.success(`Signed out ${g.client_name}`);
    load();
  }

  return (
    <>
      <PageHeader title="Access" description="How clients prove who they are. Each credential only ever sees your own MCPs.">
        <Button onClick={() => setCreating(true)}><Plus /> New token</Button>
      </PageHeader>

      <h2 className="mb-3 text-sm font-medium text-muted-foreground">Bearer tokens</h2>
      {!tokens && <Skeleton className="h-24 w-full" />}
      {tokens && tokens.length === 0 && (
        <EmptyState icon={<KeyRound />} title="No tokens">
          Tokens are for clients that can't do the browser login. Create one and send it as <code>Authorization: Bearer …</code>.
        </EmptyState>
      )}
      {tokens && tokens.length > 0 && (
        <Card className="divide-y">
          {tokens.map((t) => (
            <div key={t.id} className="flex flex-wrap items-center justify-between gap-3 p-4">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{t.name}</span>
                  <code className="text-xs text-muted-foreground">{t.prefix}…</code>
                  {t.upstream_ids === null ? (
                    <Badge variant="outline">All MCPs</Badge>
                  ) : (
                    <Badge variant="outline" title={t.upstream_ids.map((i) => names[i] ?? i).join(", ")}>
                      {t.upstream_ids.length} MCP{t.upstream_ids.length === 1 ? "" : "s"}
                    </Badge>
                  )}
                  {t.expires_at && <Badge variant="warning">Expires {new Date(t.expires_at).toLocaleDateString()}</Badge>}
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  Created {timeAgo(t.created_at)} · last used {t.last_used_at ? timeAgo(t.last_used_at) : "never"}
                </p>
              </div>
              <Button size="sm" variant="outline" onClick={() => revoke(t)}><Trash2 /> Revoke</Button>
            </div>
          ))}
        </Card>
      )}

      <h2 className="mt-10 mb-3 text-sm font-medium text-muted-foreground">Signed-in apps</h2>
      {grants && grants.length === 0 && (
        <EmptyState icon={<AppWindow />} title="No apps signed in">
          Apps that log in with OAuth (like Claude) show up here after you approve them.
        </EmptyState>
      )}
      {!grants && <Skeleton className="h-16 w-full" />}
      {grants && grants.length > 0 && (
        <Card className="divide-y">
          {grants.map((g) => (
            <div key={g.client_id} className="flex items-center justify-between gap-3 p-4">
              <div>
                <p className="font-medium">{g.client_name}</p>
                <p className="text-xs text-muted-foreground">Approved {timeAgo(g.created_at)}</p>
              </div>
              <Button size="sm" variant="outline" onClick={() => revokeGrant(g)}>Sign out</Button>
            </div>
          ))}
        </Card>
      )}

      {creating && (
        <CreateDialog ups={ups} onClose={() => setCreating(false)} onCreated={(tok) => { setCreating(false); setCreated(tok); load(); }} />
      )}
      <Dialog open={!!created} onOpenChange={(o) => !o && setCreated(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Copy your token now</DialogTitle>
            <DialogDescription>It won't be shown again. If you lose it, revoke it and make a new one.</DialogDescription>
          </DialogHeader>
          <CodeBlock code={created ?? ""} />
          <p className="text-xs text-muted-foreground">Use it with <code>{me?.mcp_url}</code> as an <code>Authorization: Bearer</code> header.</p>
          <DialogFooter><Button onClick={() => setCreated(null)}>Done</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function CreateDialog({ ups, onClose, onCreated }: { ups: Upstream[]; onClose: () => void; onCreated: (token: string) => void }) {
  const [name, setName] = useState("");
  const [scoped, setScoped] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [days, setDays] = useState("0");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (scoped && picked.length === 0) return setError("Pick at least one MCP, or allow all.");
    setBusy(true);
    setError("");
    try {
      const r = await api<{ token: string }>("/api/tokens", {
        method: "POST",
        body: { name, upstream_ids: scoped ? picked : null, expires_in_days: Number(days) },
      });
      onCreated(r.token);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New bearer token</DialogTitle>
          <DialogDescription>One token per client makes it easy to revoke just one.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field label="Name"><Input autoFocus required maxLength={80} placeholder="Claude Desktop" value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <div className="grid gap-2">
            <Label>Can use</Label>
            <NativeSelect value={scoped ? "some" : "all"} onChange={(e) => setScoped(e.target.value === "some")}>
              <option value="all">All my MCPs</option>
              <option value="some">Only the MCPs I pick</option>
            </NativeSelect>
            {scoped && (
              <div className="grid gap-2 rounded-md border p-3">
                {ups.length === 0 && <p className="text-sm text-muted-foreground">You haven't added any MCPs yet.</p>}
                {ups.map((u) => (
                  <label key={u.id} className="flex items-center gap-2 text-sm">
                    <Checkbox checked={picked.includes(u.id)} onCheckedChange={(v) => setPicked((p) => (v === true ? [...p, u.id] : p.filter((x) => x !== u.id)))} />
                    {u.name}
                  </label>
                ))}
              </div>
            )}
          </div>
          <Field label="Expires">
            <NativeSelect value={days} onChange={(e) => setDays(e.target.value)}>
              <option value="0">Never</option>
              <option value="30">In 30 days</option>
              <option value="90">In 90 days</option>
              <option value="365">In a year</option>
            </NativeSelect>
          </Field>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>Create token</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
