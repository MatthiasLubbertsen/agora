import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Plus, ShieldCheck, Trash2, UserX, UserCheck, Users as UsersIcon } from "lucide-react";
import { api, type User } from "@/lib/api";
import { useAuth } from "@/auth";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { EmptyState, PageHeader, Skeleton, FormField, LoadingButton } from "@/components/ui/misc";
import { timeAgo } from "@/lib/utils";

export function Users() {
  const { me } = useAuth();
  const [users, setUsers] = useState<User[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<User | null>(null);

  const load = () => api<User[]>("/api/admin/users").then(setUsers).catch((e) => toast.error(e.message));
  useEffect(() => { void load(); }, []);

  async function patch(u: User, body: Partial<Pick<User, "is_admin" | "disabled">>) {
    try {
      await api(`/api/admin/users/${u.id}`, { method: "PATCH", body });
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }
  async function remove(u: User) {
    try {
      await api(`/api/admin/users/${u.id}`, { method: "DELETE" });
      toast.success(`Deleted ${u.email}`);
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
    setDeleting(null);
  }

  return (
    <>
      <PageHeader title="Users" description="Add people by email. They sign in with your login provider and are matched by that email.">
        <Button onClick={() => setAdding(true)}><Plus /> Add user</Button>
      </PageHeader>
      {!users && <Skeleton className="h-40 w-full" />}
      {users && users.length === 0 && <EmptyState icon={<UsersIcon />} title="No users" />}
      {users && users.length > 0 && (
        <Card className="divide-y">
          {users.map((u) => {
            const self = u.id === me?.user.id;
            return (
              <div key={u.id} className="flex flex-wrap items-center justify-between gap-3 p-4">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-medium">{u.email}</span>
                    {u.is_admin && <Badge><ShieldCheck className="size-3" /> Admin</Badge>}
                    {u.disabled && <Badge variant="destructive">Disabled</Badge>}
                    {self && <Badge variant="outline">You</Badge>}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {u.name ? `${u.name} · ` : ""}
                    {u.last_login_at && new Date(u.last_login_at).getTime() > 86400000 ? `signed in ${timeAgo(u.last_login_at)}` : "hasn't signed in yet"}
                  </p>
                </div>
                <div className="flex items-center gap-1.5">
                  <Button size="sm" variant="outline" disabled={self} onClick={() => patch(u, { is_admin: !u.is_admin })}>
                    {u.is_admin ? "Remove admin" : "Make admin"}
                  </Button>
                  <Button size="sm" variant="outline" disabled={self} onClick={() => patch(u, { disabled: !u.disabled })}>
                    {u.disabled ? <><UserCheck /> Enable</> : <><UserX /> Disable</>}
                  </Button>
                  <Button size="icon-sm" variant="ghost" disabled={self} onClick={() => setDeleting(u)} aria-label={`Delete ${u.email}`}><Trash2 /></Button>
                </div>
              </div>
            );
          })}
        </Card>
      )}

      {adding && <AddDialog onClose={() => setAdding(false)} onAdded={() => { setAdding(false); void load(); }} />}
      <Dialog open={!!deleting} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {deleting?.email}?</DialogTitle>
            <DialogDescription>Their MCPs, tokens and sessions are deleted too. This can't be undone. To just block them, disable the account instead.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => deleting && remove(deleting)}>Delete user</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function AddDialog({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [admin, setAdmin] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/api/admin/users", { method: "POST", body: { email, name, is_admin: admin } });
      toast.success(`Added ${email}`);
      onAdded();
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a user</DialogTitle>
          <DialogDescription>They can sign in as soon as you add them, using the same email at your login provider.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <FormField label="Email"><Input type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="person@example.com" /></FormField>
          <FormField label="Name (optional)"><Input value={name} onChange={(e) => setName(e.target.value)} /></FormField>
          <div className="flex items-center gap-3">
            <Checkbox id="admin" checked={admin} onCheckedChange={(v) => setAdmin(v === true)} />
            <Label htmlFor="admin">Make this person an admin</Label>
          </div>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <LoadingButton type="submit" loading={busy}>Add user</LoadingButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
