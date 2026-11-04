import { Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider, useAuth } from "@/auth";
import { Layout } from "@/components/layout";
import { Skeleton } from "@/components/ui/misc";
import { Login, Setup } from "@/pages/login";
import { Overview } from "@/pages/overview";
import { Mcps } from "@/pages/mcps";
import { Tokens } from "@/pages/tokens";
import { Users } from "@/pages/users";
import { Settings } from "@/pages/settings";
import { Consent } from "@/pages/consent";

function Protected({ admin }: { admin?: boolean }) {
  const { me, loading } = useAuth();
  const loc = useLocation();
  if (loading) {
    return (
      <div className="mx-auto max-w-5xl space-y-4 px-4 py-10">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }
  if (!me) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
  if (admin && !me.user.is_admin) return <Navigate to="/" replace />;
  return <Outlet />;
}

export function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/setup" element={<Setup />} />
        <Route element={<Protected />}>
          <Route path="/consent" element={<Consent />} />
          <Route element={<Layout />}>
            <Route index element={<Overview />} />
            <Route path="/mcps" element={<Mcps />} />
            <Route path="/tokens" element={<Tokens />} />
            <Route element={<Protected admin />}>
              <Route path="/users" element={<Users />} />
              <Route path="/settings" element={<Settings />} />
            </Route>
          </Route>
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </AuthProvider>
  );
}
