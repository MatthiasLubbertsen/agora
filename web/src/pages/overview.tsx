import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Plug, Plus, Globe } from "lucide-react";
import { api, type Upstream } from "@/lib/api";
import { useAuth } from "@/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { CodeBlock, CopyButton, PageHeader, Skeleton } from "@/components/ui/misc";
import { cn } from "@/lib/utils";

function b64(s: string) {
  return btoa(unescape(encodeURIComponent(s)));
}

export function Overview() {
  const { me } = useAuth();
  const [ups, setUps] = useState<Upstream[] | null>(null);
  const [tab, setTab] = useState("claude");

  useEffect(() => {
    api<Upstream[]>("/api/upstreams").then(setUps).catch(() => setUps([]));
  }, []);

  if (!me) return null;
  const url = me.mcp_url;
  const tools = (ups ?? []).reduce((n, u) => n + (u.status?.tools ?? 0), 0);

  const tabs: { id: string; label: string; body: React.ReactNode }[] = [
    {
      id: "claude",
      label: "Claude",
      body: (
        <div className="grid gap-3 text-sm">
          <p className="text-muted-foreground">
            In Claude, open <b className="text-foreground">Settings → Connectors → Add custom connector</b>, paste the URL below and
            connect. A sign-in window opens; approve it and you're done.
          </p>
          <CodeBlock code={url} />
        </div>
      ),
    },
    {
      id: "claude-code",
      label: "Claude Code",
      body: (
        <div className="grid gap-3 text-sm">
          <p className="text-muted-foreground">Add it, then run <code className="text-foreground">/mcp</code> inside Claude Code and choose “Authenticate”.</p>
          <CodeBlock code={`claude mcp add --transport http gateway ${url}`} />
          <p className="text-muted-foreground">Prefer a token (no browser login)? Create one under <Link className="underline" to="/tokens">Access</Link>:</p>
          <CodeBlock code={`claude mcp add --transport http gateway ${url} \\\n  --header "Authorization: Bearer YOUR_TOKEN"`} />
        </div>
      ),
    },
    {
      id: "cursor",
      label: "Cursor",
      body: (
        <div className="grid gap-3 text-sm">
          <Button asChild className="w-fit">
            <a href={`cursor://anysphere.cursor-deeplink/mcp/install?name=gateway&config=${encodeURIComponent(b64(JSON.stringify({ url })))}`}>
              <Plus /> Add to Cursor
            </a>
          </Button>
          <p className="text-muted-foreground">Or paste into <code className="text-foreground">~/.cursor/mcp.json</code>:</p>
          <CodeBlock code={JSON.stringify({ mcpServers: { gateway: { url } } }, null, 2)} />
        </div>
      ),
    },
    {
      id: "vscode",
      label: "VS Code",
      body: (
        <div className="grid gap-3 text-sm">
          <CodeBlock code={`code --add-mcp '${JSON.stringify({ name: "gateway", type: "http", url })}'`} />
        </div>
      ),
    },
    {
      id: "token",
      label: "Any client",
      body: (
        <div className="grid gap-3 text-sm">
          <p className="text-muted-foreground">
            Any MCP client that supports Streamable HTTP works. Use OAuth if it supports it, or send a bearer token from{" "}
            <Link className="underline" to="/tokens">Access</Link>.
          </p>
          <CodeBlock
            code={JSON.stringify({ mcpServers: { gateway: { url, headers: { Authorization: "Bearer YOUR_TOKEN" } } } }, null, 2)}
          />
        </div>
      ),
    },
  ];

  return (
    <>
      <PageHeader title="Overview" description="One endpoint for every MCP you add." />

      <Card className="mb-6">
        <CardHeader>
          <CardTitle>Your gateway URL</CardTitle>
          <CardDescription>Give this to any MCP client. It sees only your MCPs, based on how it signs in.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5">
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded-md border bg-muted/60 px-3 py-2 text-[13px]">{url}</code>
            <CopyButton text={url} label="Copy" />
          </div>
          <div className="grid grid-cols-3 gap-3 text-sm">
            <Stat label="MCPs" value={ups ? String(ups.filter((u) => u.enabled).length) : null} />
            <Stat label="Tools" value={ups ? String(tools) : null} />
            <Stat label="Public MCPs" value={ups ? String(ups.filter((u) => u.is_public).length) : null} />
          </div>
        </CardContent>
      </Card>

      {ups && ups.length === 0 && (
        <Card className="mb-6 border-dashed">
          <CardContent className="flex flex-wrap items-center justify-between gap-3 p-5">
            <div className="flex items-center gap-3">
              <Plug className="size-5 text-muted-foreground" />
              <div>
                <p className="font-medium">No MCPs yet</p>
                <p className="text-sm text-muted-foreground">Add the first one so your clients have something to talk to.</p>
              </div>
            </div>
            <Button asChild><Link to="/mcps"><Plus /> Add an MCP</Link></Button>
          </CardContent>
        </Card>
      )}

      <Card className="mb-6">
        <CardHeader>
          <CardTitle>Connect a client</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div role="tablist" className="flex gap-1 overflow-x-auto rounded-md border bg-muted/40 p-1">
            {tabs.map((t) => (
              <button
                key={t.id}
                role="tab"
                aria-selected={tab === t.id}
                onClick={() => setTab(t.id)}
                className={cn(
                  "rounded px-3 py-1.5 text-sm whitespace-nowrap transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
                  tab === t.id ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {t.label}
              </button>
            ))}
          </div>
          <div role="tabpanel">{tabs.find((t) => t.id === tab)?.body}</div>
        </CardContent>
      </Card>

      {me.allow_anonymous && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2"><Globe className="size-4" /> Public endpoint (no sign-in)</CardTitle>
            <CardDescription>
              Anyone with this URL can use the MCPs marked <b className="text-foreground">Public</b>. Nothing else is exposed.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CodeBlock code={me.public_mcp_url} />
          </CardContent>
        </Card>
      )}
    </>
  );
}

function Stat({ label, value }: { label: string; value: string | null }) {
  return (
    <div className="rounded-md border p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      {value === null ? <Skeleton className="mt-1 h-7 w-10" /> : <p className="text-2xl font-semibold tabular-nums">{value}</p>}
    </div>
  );
}
