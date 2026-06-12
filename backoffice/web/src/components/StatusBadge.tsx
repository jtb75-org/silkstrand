interface StatusBadgeProps {
  status: string;
}

const statusStyles: Record<string, { bg: string; color: string }> = {
  healthy: { bg: '#d1fae5', color: '#065f46' },
  active: { bg: '#d1fae5', color: '#065f46' },
  ready: { bg: '#d1fae5', color: '#065f46' },
  degraded: { bg: '#fef3c7', color: '#92400e' },
  provisioning: { bg: '#dbeafe', color: '#1d4ed8' },
  pending: { bg: '#dbeafe', color: '#1d4ed8' },
  invited: { bg: '#fef3c7', color: '#92400e' },
  offline: { bg: '#fee2e2', color: '#991b1b' },
  suspended: { bg: '#fee2e2', color: '#991b1b' },
  failed: { bg: '#fee2e2', color: '#991b1b' },
  unhealthy: { bg: '#fee2e2', color: '#991b1b' },
  unknown: { bg: '#e5e7eb', color: '#4b5563' },
};

export default function StatusBadge({ status }: StatusBadgeProps) {
  const style = statusStyles[status] || { bg: '#e5e7eb', color: '#4b5563' };

  return (
    <span
      className="badge"
      style={{ backgroundColor: style.bg, color: style.color }}
    >
      {status}
    </span>
  );
}
