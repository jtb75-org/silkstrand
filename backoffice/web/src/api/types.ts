export type DCEnvironment = 'stage' | 'prod';

export interface DataCenter {
  id: string;
  name: string;
  region: string;
  environment: DCEnvironment;
  api_url: string;
  status: 'active' | 'inactive';
  last_health_status: string; // 'healthy' | 'unhealthy' | 'unknown'
  last_health_check?: string;
  tenant_count: number;
  created_at: string;
  updated_at: string;
}

export interface Tenant {
  id: string;
  data_center_id: string;
  dc_tenant_id: string;
  name: string;
  status: 'active' | 'suspended';
  provisioning_status: 'pending' | 'provisioning' | 'ready' | 'failed';
  config?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  invite_results?: InviteResult[];
}

export type InviteRole = 'admin' | 'member';

export interface TenantInvite {
  email: string;
  role: InviteRole;
}

export interface InviteResult {
  email: string;
  role: InviteRole;
  status: 'invited' | 'failed';
  error?: string;
}

export interface AdminUser {
  id: string;
  email: string;
  name: string;
  role: string;
}

export interface DashboardData {
  total_data_centers: number;
  total_tenants: number;
  active_tenants: number;
  suspended_tenants: number;
  data_centers: DataCenter[];
}

export interface LoginRequest {
  email: string;
  password: string;
}

export interface LoginResponse {
  token: string;
  admin: AdminUser;
}

export interface CreateDataCenterRequest {
  name: string;
  region: string;
  environment: DCEnvironment;
  api_url: string;
  api_key: string;
}

export interface UpdateDataCenterRequest {
  name?: string;
  region?: string;
  environment?: DCEnvironment;
  api_url?: string;
  api_key?: string;
}

export interface CreateTenantRequest {
  data_center_id: string;
  name: string;
  invites?: TenantInvite[];
}

export interface UpdateTenantRequest {
  name?: string;
}

export interface UpdateTenantStatusRequest {
  status: 'active' | 'suspended';
}

// Users (cross-tenant backoffice view)

export interface User {
  id: string;
  email: string;
  status: 'active' | 'suspended';
  email_verified_at?: string | null;
  last_login_at?: string | null;
  tenant_count: number;
  created_at: string;
  updated_at: string;
}

export interface UserMembership {
  tenant_id: string;
  tenant_name: string;
  data_center_id: string;
  dc_name: string;
  environment: string;
  role: 'admin' | 'member';
  status: 'active' | 'suspended';
  created_at: string;
}

export interface UserPendingInvite {
  id: string;
  email: string;
  role: 'admin' | 'member';
  expires_at: string;
  created_at: string;
}

export interface UserDetail extends User {
  memberships: UserMembership[];
  pending_invites: UserPendingInvite[];
}

// Tenant detail — Users tab (backoffice admin view of one tenant)

export interface TenantMember {
  user_id: string;
  email: string;
  role: 'admin' | 'member';
  status: 'active' | 'suspended';
  created_at: string;
}

export interface TenantPendingInvite {
  id: string;
  email: string;
  role: 'admin' | 'member';
  expires_at: string;
  created_at: string;
}

export interface AuditEntry {
  id: string;
  occurred_at: string;
  actor_type: 'admin' | 'tenant_user' | 'system';
  actor_id?: string;
  actor_email?: string;
  action: string;
  target_type?: string;
  target_id?: string;
  tenant_id?: string;
  ip?: string;
  metadata?: Record<string, unknown>;
}
