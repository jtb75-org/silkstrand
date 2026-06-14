import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthProvider } from './auth/AuthContext';
import { ToastProvider } from './components/Toast';
import ProtectedRoute from './components/ProtectedRoute';
import Layout from './components/Layout';
import Dashboard from './pages/Dashboard';
import Targets from './pages/Targets';
import Assets from './pages/Assets';
import CorrelationRules from './pages/CorrelationRules';
import Collections from './pages/Collections';
import Agents from './pages/Agents';
import Scans from './pages/Scans';
import ScanResults from './pages/ScanResults';
import Findings from './pages/Findings';
import Compliance from './pages/Compliance';
import Team from './pages/Team';
import Settings from './pages/Settings';
import AuditLog from './pages/AuditLog';
import Login from './pages/Login';
import AcceptInvite from './pages/AcceptInvite';
import ForgotPassword from './pages/ForgotPassword';
import ResetPassword from './pages/ResetPassword';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: 1, refetchOnWindowFocus: false },
  },
});

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
      <BrowserRouter>
        <AuthProvider>
          <Routes>
            {/* Public auth routes */}
            <Route path="/login" element={<Login />} />
            <Route path="/accept-invite" element={<AcceptInvite />} />
            <Route path="/forgot-password" element={<ForgotPassword />} />
            <Route path="/reset-password" element={<ResetPassword />} />

            {/* Protected app */}
            <Route element={<ProtectedRoute><Layout /></ProtectedRoute>}>
              <Route path="/" element={<Dashboard />} />
              <Route path="/assets" element={<Assets />} />
              <Route path="/assets/:id" element={<Assets />} />
              <Route path="/targets" element={<Targets />} />
              <Route path="/agents" element={<Agents />} />
              <Route path="/scans" element={<Scans />} />
              <Route path="/scans/*" element={<Scans />} />
              <Route path="/scans/:id" element={<ScanResults />} />
              <Route path="/findings" element={<Findings />} />
              <Route path="/compliance" element={<Compliance />} />
              <Route path="/team" element={<Team />} />
              <Route path="/settings" element={<Settings />} />
              <Route path="/audit" element={<AuditLog />} />
              <Route path="/rules" element={<CorrelationRules />} />
              <Route path="/collections" element={<Collections />} />
            </Route>
          </Routes>
        </AuthProvider>
      </BrowserRouter>
      </ToastProvider>
    </QueryClientProvider>
  );
}
