import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { Globe, Link2, ListTree, Pencil, Plug, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, type Status, type Upstream } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { EmptyState, PageHeader, Skeleton } from "@/components/ui/misc";
import { Switch } from "@/components/ui/switch";
import { cn, timeAgo } from "@/lib/utils";

function slugify(s: string) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 32).replace(/-+$/, "");
}

export function Mcps() {
  const [list, setList] = useState<Upstream[] | null>(null);
  const [editing, setEditing] = useState<Upstream | "new" | null>(null);
  const [deleting, setDeleting] = useState<Upstream | null>(null);
  const [catalog, setCatalog] = useState<Upstream | null>(null);
  const [connecting, setConnecting] = useState<{ up: Upstream; error: string } | null>(null);
  const [params, setParams] = useSearchParams();
  const tested = useRef(new Set<string>());

  const load = useCallback(async () => {
    setList(await api<Upstream[]>("/api/upstreams"));
  }, []);

  const patch = (id: string, f: Partial<Upstream>) => setList((l) => l && l.map((u) => (u.id === id ? { ...u, ...f } : u)));

  const test = useCallback(async (u: Upstream, quiet = false) => {
    patch(u.id, { status: null });
    try {
      const st = await api<Status>(`/api/upstreams/${u.id}/test`, { method: "POST" });
      patch(u.id, { status: st });
      if (!quiet) st.ok ? toast.success(`${u.name}: ${st.tools} tools`) : toast.error(st.error ?? "Couldn't connect");
    } catch (e) {
      if (!quiet) toast.error((e as Error).message);
    }
  }, []);

  useEffect(() => {
    load().catch((e) => toast.error(e.message));
  }, [load]);

  // Check each MCP once when the page opens so the status dots are meaningful.
  useEffect(() => {
    if (!list) return;
    for (const u of list) {
      if (tested.current.has(u.id) || u.status || !u.enabled) continue;
      if (u.auth_type === "oauth" && !u.oauth_connected) continue;
      tested.current.add(u.id);
      void test(u, true);
    }
  }, [list, test]);

  // Coming back from an upstream's login page.
  useEffect(() => {
    const err = params.get("error");
    const ok = params.get("connected");
    if (!err && !ok) return;
    if (err) toast.error(err);
    if (ok) toast.success("Connected");
    setParams({}, { replace: true });
    if (ok) load().then(() => undefined);
  }, [params, setParams, load]);

  async function connect(u: Upstream, creds?: { client_id: string; client_secret: string }) {
    try {
      const r = await api<{ url: string }>(`/api/upstreams/${u.id}/oauth/start`, { method: "POST", body: creds ?? {} });
      window.location.href = r.url;
    } catch (e) {
      setConnecting({ up: u, error: (e as Error).message });
    }
  }

  async function toggle(u: Upstream, f: Partial<Pick<Upstream, "enabled" | "is_public">>) {
    const prev = { enabled: u.enabled, is_public: u.is_public };
    patch(u.id, f);
    try {
      await api(`/api/upstreams/${u.id}`, { method: "PATCH", body: f });
    } catch (e) {
      patch(u.id, prev);
      toast.error((e as Error).message);
    }
  }

  async function remove(u: Upstream) {
    try {
      await api(`/api/upstreams/${u.id}`, { method: "DELETE" });
      setList((l) => l && l.filter((x) => x.id !== u.id));
      toast.success(`Removed ${u.name}`);
    } catch (e) {
      toast.error((e as Error).message);
    }
    setDeleting(null);
  }

  return (
    <>
      <PageHeader title="MCPs" description="The servers your gateway fronts. Each one's tools appear as slug__tool.">
        <Button onClick={() => setEditing("new")}><Plus /> Add MCP</Button>
      </PageHeader>

      {!list && (
        <div className="grid gap-3">
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-28 w-full" />
        </div>
      )}
      {list && list.length === 0 && (
        <EmptyState icon={<Plug />} title="No MCPs yet">
          Add any remote MCP server by URL: Notion, Linear, GitHub, your own, anything that speaks Streamable HTTP or SSE.
          <div className="mt-4"><Button onClick={() => setEditing("new")}><Plus /> Add your first MCP</Button></div>
        </EmptyState>
      )}
      <div className="grid gap-3">
        {list?.map((u) => (
          <Row key={u.id} u={u} onTest={() => test(u)} onEdit={() => setEditing(u)} onDelete={() => setDeleting(u)}
            onCatalog={() => setCatalog(u)} onConnect={() => connect(u)} onToggle={(f) => toggle(u, f)} />
        ))}
      </div>

      {editing && (
        <UpstreamForm
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async (saved, isNew) => {
            setEditing(null);
            tested.current.delete(saved.id);
            await load();
            if (saved.auth_type === "oauth" && !saved.oauth_connected) {
              toast.info("Now connect it to log in to this MCP.");
            } else if (isNew) {
              tested.current.add(saved.id);
              void test(saved, false);
            }
          }}
        />
      )}

      <Dialog open={!!deleting} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove {deleting?.name}?</DialogTitle>
            <DialogDescription>Its tools disappear from your gateway straight away. Any stored login or key is deleted.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => deleting && remove(deleting)}>Remove</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Catalog up={catalog} onClose={() => setCatalog(null)} />
      <ConnectDialog state={connecting} onClose={() => setConnecting(null)} onSubmit={(c) => connecting && connect(connecting.up, c)} />
    </>
  );
}

function StatusLine({ u }: { u: Upstream }) {
  const st = u.status;
  if (!u.enabled) return <Dot color="bg-muted-foreground/50" text="Disabled" />;
  if (u.auth_type === "oauth" && !u.oauth_connected) return <Dot color="bg-warning" text="Needs login" />;
  if (!st) return <Dot color="bg-muted-foreground/50 animate-pulse" text="Checking…" />;
  if (st.ok) {
    const bits = [`${st.tools} tool${st.tools === 1 ? "" : "s"}`];
    if (st.prompts) bits.push(`${st.prompts} prompt${st.prompts === 1 ? "" : "s"}`);
    if (st.resources) bits.push(`${st.resources} resource${st.resources === 1 ? "" : "s"}`);
    return <Dot color="bg-success" text={`Connected · ${bits.join(" · ")}`} sub={`checked ${timeAgo(st.checked_at)}`} />;
  }
  return <Dot color={st.needs_auth ? "bg-warning" : "bg-destructive"} text={st.needs_auth ? "Login needed or expired" : "Can't connect"} sub={st.error} />;
}

function Dot({ color, text, sub }: { color: string; text: string; sub?: string }) {
  return (
    <div className="min-w-0 text-sm">
      <span className="inline-flex items-center gap-2">
        <span className={cn("size-2 shrink-0 rounded-full", color)} />
        <span>{text}</span>
      </span>
      {sub && <p className="mt-0.5 truncate pl-4 text-xs text-muted-foreground" title={sub}>{sub}</p>}
    </div>
  );
}

function Row(p: {
  u: Upstream; onTest: () => void; onEdit: () => void; onDelete: () => void; onCatalog: () => void; onConnect: () => void;
  onToggle: (f: Partial<Pick<Upstream, "enabled" | "is_public">>) => void;
}) {
  const { u } = p;
  const authLabel = u.auth_type === "oauth" ? "OAuth" : u.auth_type === "header" ? "Bearer token" : "No auth";
  return (
    <Card className={cn("p-4", !u.enabled && "opacity-70")}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-semibold">{u.name}</h3>
            <code className="rounded bg-accent px-1.5 py-0.5 text-xs text-muted-foreground">{u.slug}__</code>
            <Badge variant="outline">{u.transport === "sse" ? "SSE" : "HTTP"}</Badge>
            <Badge variant="outline">{authLabel}</Badge>
            {u.is_public && <Badge variant="warning"><Globe className="size-3" /> Public</Badge>}
          </div>
          <p className="mt-1 truncate font-mono text-[13px] text-muted-foreground" title={u.url}>{u.url}</p>
        </div>
        <Switch checked={u.enabled} onCheckedChange={(v) => p.onToggle({ enabled: v })} aria-label={`Enable ${u.name}`} />
      </div>
      <div className="mt-3 flex flex-wrap items-center justify-between gap-3 border-t pt-3">
        <StatusLine u={u} />
        <div className="flex flex-wrap items-center gap-1.5">
          {u.auth_type === "oauth" && (
            <Button size="sm" variant={u.oauth_connected ? "outline" : "default"} onClick={p.onConnect}>
              <Link2 /> {u.oauth_connected ? "Reconnect" : "Connect"}
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={p.onTest}><RefreshCw /> Test</Button>
          <Button size="sm" variant="outline" onClick={p.onCatalog}><ListTree /> Tools</Button>
          <Button size="icon-sm" variant="ghost" onClick={p.onEdit} aria-label="Edit"><Pencil /></Button>
          <Button size="icon-sm" variant="ghost" onClick={p.onDelete} aria-label="Remove"><Trash2 /></Button>
        </div>
      </div>
      <label className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
        <Switch checked={u.is_public} onCheckedChange={(v) => p.onToggle({ is_public: v })} className="scale-90" />
        Public: reachable without signing in via the public endpoint (only if an admin enables it)
      </label>
    </Card>
  );
}

function UpstreamForm({ initial, onClose, onSaved }: { initial: Upstream | null; onClose: () => void; onSaved: (u: Upstream, isNew: boolean) => void }) {
  const [name, setName] = useState(initial?.name ?? "");
  const [slug, setSlug] = useState(initial?.slug ?? "");
  const [slugTouched, setSlugTouched] = useState(!!initial);
  const [url, setUrl] = useState(initial?.url ?? "");
  const [transport, setTransport] = useState(initial?.transport ?? "http");
  const [authType, setAuthType] = useState(initial?.auth_type ?? "none");
  const [headerName, setHeaderName] = useState(initial?.header_name ?? "");
  const [secret, setSecret] = useState("");
  const [isPublic, setIsPublic] = useState(initial?.is_public ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const body = { name, slug: slug || slugify(name), url, transport, auth_type: authType, header_name: headerName, secret, is_public: isPublic };
    try {
      const saved = initial
        ? await api<Upstream>(`/api/upstreams/${initial.id}`, { method: "PATCH", body })
        : await api<Upstream>("/api/upstreams", { method: "POST", body });
      onSaved(saved, !initial);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{initial ? `Edit ${initial.name}` : "Add an MCP"}</DialogTitle>
          <DialogDescription>Point the gateway at a remote MCP server.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field label="Name">
            <Input value={name} autoFocus required placeholder="Notion" maxLength={80}
              onChange={(e) => { setName(e.target.value); if (!slugTouched) setSlug(slugify(e.target.value)); }} />
          </Field>
          <Field label="Server URL" hint="The MCP endpoint, e.g. https://mcp.notion.com/mcp">
            <Input value={url} required type="url" placeholder="https://…" onChange={(e) => setUrl(e.target.value)} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Short name" hint={<>Tools become <code>{slug || "name"}__tool</code></>}>
              <Input value={slug} maxLength={32} pattern="[a-z0-9][a-z0-9\-]*" placeholder={slugify(name) || "notion"}
                onChange={(e) => { setSlug(e.target.value.toLowerCase()); setSlugTouched(true); }} />
            </Field>
            <Field label="Transport">
              <NativeSelect value={transport} onChange={(e) => setTransport(e.target.value as "http" | "sse")}>
                <option value="http">Streamable HTTP</option>
                <option value="sse">SSE (legacy)</option>
              </NativeSelect>
            </Field>
          </div>
          <Field label="Authentication">
            <NativeSelect value={authType} onChange={(e) => setAuthType(e.target.value as Upstream["auth_type"])}>
              <option value="none">None</option>
              <option value="header">Bearer token (your own)</option>
              <option value="oauth">OAuth (log in to it)</option>
            </NativeSelect>
          </Field>
          {authType === "header" && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Header name" hint="Leave empty for Authorization">
                <Input value={headerName} placeholder="Authorization" onChange={(e) => setHeaderName(e.target.value)} />
              </Field>
              <Field label="Your token" hint={initial?.has_secret ? "Leave empty to keep the saved one. Only you can use it." : "Sent as “Authorization: Bearer …” on every request. Stored encrypted, only you can use it."}>
                <Input type="password" autoComplete="off" value={secret} required={!initial?.has_secret} onChange={(e) => setSecret(e.target.value)} />
              </Field>
            </div>
          )}
          {authType === "oauth" && (
            <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
              After saving, press <b className="text-foreground">Connect</b> to log in to the MCP. The gateway stores and refreshes the login for you.
            </p>
          )}
          <div className="flex items-start gap-3">
            <Checkbox id="public" checked={isPublic} onCheckedChange={(v) => setIsPublic(v === true)} className="mt-0.5" />
            <div>
              <Label htmlFor="public">Make public</Label>
              <p className="text-xs text-muted-foreground">Anyone can use this MCP through the public endpoint, with your credentials for it. Off by default.</p>
            </div>
          </div>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>{initial ? "Save" : "Add MCP"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Catalog({ up, onClose }: { up: Upstream | null; onClose: () => void }) {
  const [data, setData] = useState<Record<string, { name: string; gateway_name?: string; description: string }[]> | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setData(null);
    setError("");
    if (!up) return;
    api<NonNullable<typeof data>>(`/api/upstreams/${up.id}/catalog`).then(setData).catch((e) => setError(e.message));
  }, [up]);
  const sections = useMemo(() => (data ? (["tools", "prompts", "resources"] as const).filter((k) => data[k].length) : []), [data]);
  return (
    <Dialog open={!!up} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{up?.name}</DialogTitle>
          <DialogDescription>What this MCP exposes through the gateway.</DialogDescription>
        </DialogHeader>
        {error && <p className="text-sm text-destructive">{error}</p>}
        {!data && !error && <Skeleton className="h-32 w-full" />}
        {data && sections.length === 0 && <p className="text-sm text-muted-foreground">Nothing exposed.</p>}
        {sections.map((k) => (
          <div key={k}>
            <h4 className="mb-2 text-xs font-medium tracking-wide text-muted-foreground uppercase">{k}</h4>
            <ul className="divide-y rounded-md border">
              {data![k].map((i) => (
                <li key={i.name} className="px-3 py-2">
                  <code className="text-[13px]">{i.gateway_name ?? i.name}</code>
                  {i.description && <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{i.description}</p>}
                </li>
              ))}
            </ul>
          </div>
        ))}
      </DialogContent>
    </Dialog>
  );
}

function ConnectDialog({ state, onClose, onSubmit }: { state: { up: Upstream; error: string } | null; onClose: () => void; onSubmit: (c: { client_id: string; client_secret: string }) => void }) {
  const [id, setId] = useState("");
  const [secret, setSecret] = useState("");
  return (
    <Dialog open={!!state} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Couldn't connect {state?.up.name}</DialogTitle>
          <DialogDescription>{state?.error}</DialogDescription>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); onSubmit({ client_id: id, client_secret: secret }); }} className="grid gap-4">
          <p className="text-sm text-muted-foreground">
            If the MCP doesn't support automatic setup, create an OAuth app on its side with redirect URL{" "}
            <code className="break-all text-foreground">{location.origin}/oauth/upstream/callback</code> and enter its credentials.
          </p>
          <Field label="Client ID"><Input value={id} required onChange={(e) => setId(e.target.value)} /></Field>
          <Field label="Client secret (if it has one)"><Input type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} /></Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit">Try again</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
