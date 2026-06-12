import type {
  DataCenter,
  Tenant,
  DashboardData,
  LoginRequest,
  LoginResponse,
  CreateDataCenterRequest,
  UpdateDataCenterRequest,
  CreateTenantRequest,
  UpdateTenantRequest,
  UpdateTenantStatusRequest,
  TenantInvite,
} from './types';

const BASE_URL = import.meta.env.VITE_API_URL || '';
const TOKEN_KEY = 'backoffice_token';

function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function hasToken(): boolean {
  return getToken() !== null;
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string> | undefined),
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    ...init,
    headers,
  });

  if (res.status === 401) {
    clearToken();
    window.location.href = '/login';
    throw new Error('Unauthorized');
  }

  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error) {
        message = body.error;
      }
    } catch {
      // ignore parse errors
    }
    throw new Error(message);
  }

  if (res.status === 204) {
    return undefined as T;
  }

  return res.json() as Promise<T>;
}

// Auth

export function login(req: LoginRequest): Promise<LoginResponse> {
  return request<LoginResponse>('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

// Dashboard

export function getDashboard(): Promise<DashboardData> {
  return request<DashboardData>('/api/v1/dashboard');
}

// Data Centers

export function listDataCenters(): Promise<DataCenter[]> {
  return request<DataCenter[]>('/api/v1/data-centers');
}

export function getDataCenter(id: string): Promise<DataCenter> {
  return request<DataCenter>(`/api/v1/data-centers/${id}`);
}

export function createDataCenter(req: CreateDataCenterRequest): Promise<DataCenter> {
  return request<DataCenter>('/api/v1/data-centers', {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

export function updateDataCenter(id: string, req: UpdateDataCenterRequest): Promise<DataCenter> {
  return request<DataCenter>(`/api/v1/data-centers/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });
}

export function deleteDataCenter(id: string): Promise<void> {
  return request<void>(`/api/v1/data-centers/${id}`, {
    method: 'DELETE',
  });
}

// Tenants

export function listTenants(dataCenterId?: string): Promise<Tenant[]> {
  const params = dataCenterId ? `?data_center_id=${dataCenterId}` : '';
  return request<Tenant[]>(`/api/v1/tenants${params}`);
}

export function getTenant(id: string): Promise<Tenant> {
  return request<Tenant>(`/api/v1/tenants/${id}`);
}

export function createTenant(req: CreateTenantRequest): Promise<Tenant> {
  return request<Tenant>('/api/v1/tenants', {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

export function updateTenant(id: string, req: UpdateTenantRequest): Promise<Tenant> {
  return request<Tenant>(`/api/v1/tenants/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });
}

export function updateTenantStatus(id: string, req: UpdateTenantStatusRequest): Promise<Tenant> {
  return request<Tenant>(`/api/v1/tenants/${id}/status`, {
    method: 'PUT',
    body: JSON.stringify(req),
  });
}

export function retryTenantProvisioning(id: string): Promise<Tenant> {
  return request<Tenant>(`/api/v1/tenants/${id}/retry`, {
    method: 'POST',
  });
}

export function deleteTenant(id: string): Promise<void> {
  return request<void>(`/api/v1/tenants/${id}`, {
    method: 'DELETE',
  });
}

// Tenant members + invites (backoffice admin view of one tenant)

export function listTenantMembers(
  tenantId: string,
): Promise<import('./types').TenantMember[]> {
  return request(`/api/v1/tenants/${tenantId}/members`);
}

export function listTenantInvites(
  tenantId: string,
): Promise<import('./types').TenantPendingInvite[]> {
  return request(`/api/v1/tenants/${tenantId}/invites`);
}

export function createTenantInvite(
  tenantId: string, req: TenantInvite,
): Promise<{ status: string; error?: string }> {
  return request(`/api/v1/tenants/${tenantId}/invites`, {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

export function resendTenantInvite(tenantId: string, inviteId: string): Promise<void> {
  return request(`/api/v1/tenants/${tenantId}/invites/${inviteId}/resend`, {
    method: 'POST',
  });
}

export function deleteTenantInvite(tenantId: string, inviteId: string): Promise<void> {
  return request(`/api/v1/tenants/${tenantId}/invites/${inviteId}`, {
    method: 'DELETE',
  });
}

// Users (cross-tenant management)

export function listUsers(): Promise<import('./types').User[]> {
  return request('/api/v1/users');
}

export function getUser(id: string): Promise<import('./types').UserDetail> {
  return request(`/api/v1/users/${id}`);
}

export function updateUserStatus(id: string, status: 'active' | 'suspended'): Promise<void> {
  return request(`/api/v1/users/${id}/status`, {
    method: 'PUT',
    body: JSON.stringify({ status }),
  });
}

export function deleteUser(id: string): Promise<void> {
  return request(`/api/v1/users/${id}`, { method: 'DELETE' });
}

export function updateUserMembershipStatus(
  userId: string, tenantId: string, status: 'active' | 'suspended',
): Promise<void> {
  return request(`/api/v1/users/${userId}/memberships/${tenantId}/status`, {
    method: 'PUT',
    body: JSON.stringify({ status }),
  });
}

export function removeUserMembership(userId: string, tenantId: string): Promise<void> {
  return request(`/api/v1/users/${userId}/memberships/${tenantId}`, { method: 'DELETE' });
}

// Audit log
export function listAuditLog(
  filter: { tenant_id?: string; actor_id?: string; action?: string; limit?: number } = {},
): Promise<import('./types').AuditEntry[]> {
  const p = new URLSearchParams();
  if (filter.tenant_id) p.set('tenant_id', filter.tenant_id);
  if (filter.actor_id) p.set('actor_id', filter.actor_id);
  if (filter.action) p.set('action', filter.action);
  if (filter.limit) p.set('limit', String(filter.limit));
  const q = p.toString();
  return request(`/api/v1/audit${q ? `?${q}` : ''}`);
}
