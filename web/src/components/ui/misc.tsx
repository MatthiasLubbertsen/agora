import * as React from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "./button";
import { cn } from "@/lib/utils";

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-accent/60", className)} />;
}

export function useCopy() {
  const [copied, setCopied] = React.useState(false);
  const copy = React.useCallback(async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
    }
    setCopied(true);
    toast.success("Copied");
    setTimeout(() => setCopied(false), 1500);
  }, []);
  return { copied, copy };
}

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { copied, copy } = useCopy();
  return (
    <Button variant="outline" size="sm" onClick={() => copy(text)} aria-label="Copy">
      {copied ? <Check /> : <Copy />}
      {label}
    </Button>
  );
}

export function CodeBlock({ code, className }: { code: string; className?: string }) {
  return (
    <div className={cn("group relative rounded-md border bg-muted/60", className)}>
      <pre className="overflow-x-auto p-3 pr-14 text-[13px] leading-relaxed">
        <code>{code}</code>
      </pre>
      <div className="absolute top-2 right-2">
        <CopyButton text={code} />
      </div>
    </div>
  );
}

export function EmptyState({ icon, title, children }: { icon: React.ReactNode; title: string; children?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-6 py-14 text-center">
      <div className="flex size-10 items-center justify-center rounded-full bg-accent text-muted-foreground [&_svg]:size-5">{icon}</div>
      <div>
        <p className="font-medium">{title}</p>
        <div className="mx-auto mt-1 max-w-sm text-sm text-muted-foreground">{children}</div>
      </div>
    </div>
  );
}

export function PageHeader({ title, description, children }: { title: string; description?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      <div className="flex items-center gap-2">{children}</div>
    </div>
  );
}
