import { NavLink, Outlet } from "react-router-dom";
import { LogOut } from "lucide-react";
import { useAuth } from "@/auth";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={cn("size-6", className)} aria-hidden>
      <path d="M16 6 27 25H5z" fill="currentColor" />
    </svg>
  );
}

export function Layout() {
  const { me, logout } = useAuth();
  const items = [
    { to: "/", label: "Overview", end: true },
    { to: "/mcps", label: "MCPs" },
    { to: "/tokens", label: "Access" },
    ...(me?.user.is_admin ? [{ to: "/users", label: "Users" }, { to: "/settings", label: "Settings" }] : []),
  ];
  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-40 border-b bg-background/80 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-5xl items-center gap-6 px-4">
          <NavLink to="/" className="flex items-center gap-2 font-semibold">
            <Logo />
            <span className="hidden sm:inline">MCP Gateway</span>
          </NavLink>
          <nav className="flex flex-1 items-center gap-1 overflow-x-auto">
            {items.map((i) => (
              <NavLink
                key={i.to}
                to={i.to}
                end={i.end}
                className={({ isActive }) =>
                  cn(
                    "rounded-md px-3 py-1.5 text-sm transition-colors",
                    isActive ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground",
                  )
                }
              >
                {i.label}
              </NavLink>
            ))}
          </nav>
          <div className="flex items-center gap-2">
            <span className="hidden max-w-[200px] truncate text-sm text-muted-foreground md:inline">{me?.user.email}</span>
            <Button variant="ghost" size="icon-sm" onClick={logout} aria-label="Sign out" title="Sign out">
              <LogOut />
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-5xl px-4 py-8">
        <Outlet />
      </main>
    </div>
  );
}
