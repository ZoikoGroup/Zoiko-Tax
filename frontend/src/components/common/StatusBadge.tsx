import React from 'react';

export interface StatusBadgeProps {
  status: string;
  variant?: 'teal' | 'copper' | 'purple' | 'amber' | 'red' | 'slate';
  size?: 'sm' | 'md';
}

export const StatusBadge: React.FC<StatusBadgeProps> = ({ status, variant, size = 'sm' }) => {
  let color = variant;
  if (!color) {
    const s = status.toUpperCase();
    if (s.includes('COMMITTED') || s.includes('CERTIFIED') || s.includes('ACTIVE') || s.includes('MATCHED') || s.includes('ACCEPTED') || s.includes('PERFECT') || s.includes('READY')) {
      color = 'teal';
    } else if (s.includes('QUOTE') || s.includes('RELEASED') || s.includes('REGISTERED') || s.includes('DIRECT')) {
      color = 'copper';
    } else if (s.includes('AI_') || s.includes('VOIP') || s.includes('5G') || s.includes('ONTOLOGY')) {
      color = 'purple';
    } else if (s.includes('REVIEW') || s.includes('MONITOR') || s.includes('TESTING') || s.includes('ADVISORY') || s.includes('PENDING')) {
      color = 'amber';
    } else if (s.includes('FLAG') || s.includes('OVERDUE') || s.includes('REVERS') || s.includes('DISPUTE') || s.includes('ACTION_REQUIRED') || s.includes('UNRECONCILED')) {
      color = 'red';
    } else {
      color = 'slate';
    }
  }

  // Official zoikotax.com color schemes
  const styles = {
    teal: 'bg-[#26735b]/20 text-[#34d399] border-[#26735b]/40',
    copper: 'bg-[#bf6735]/20 text-[#ff9a52] border-[#bf6735]/40',
    purple: 'bg-[#2a1a6b]/30 text-[#d8b4fe] border-[#5b2a86]/50',
    amber: 'bg-[#9a5b12]/20 text-[#fbbf24] border-[#9a5b12]/40',
    red: 'bg-[#ef4444]/15 text-[#f87171] border-[#ef4444]/30',
    slate: 'bg-[#1f1352]/50 text-[#c7c3d6] border-[#38276b]/50',
  };

  const sizeClasses = size === 'sm' ? 'px-2 py-0.5 text-xs font-mono' : 'px-2.5 py-1 text-sm font-mono';

  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-md border font-medium uppercase tracking-wider ${styles[color]} ${sizeClasses}`}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current opacity-90" />
      {status.replace(/_/g, ' ')}
    </span>
  );
};
