import React from 'react';
import { ArrowUpRight, ArrowDownRight, Minus } from 'lucide-react';

export interface StatCardProps {
  title: string;
  value: string;
  subValue?: string;
  trend?: 'up' | 'down' | 'neutral';
  trendPercent?: string;
  description?: string;
  badge?: string;
  icon?: React.ReactNode;
}

export const StatCard: React.FC<StatCardProps> = ({
  title,
  value,
  subValue,
  trend,
  trendPercent,
  description,
  badge,
  icon,
}) => {
  return (
    <div className="relative overflow-hidden rounded-xl border theme-card p-5 transition-all hover:border-[#dd7134]/50">
      <div className="flex items-start justify-between">
        <span className="text-xs font-semibold uppercase tracking-wider theme-text-muted">
          {title}
        </span>
        <div className="flex items-center gap-2">
          {badge && (
            <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 text-[10px] font-semibold tracking-wide text-[#dd7134] border border-[#bf6735]/30">
              {badge}
            </span>
          )}
          {icon && <div className="theme-text-muted">{icon}</div>}
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-baseline justify-between gap-2">
        <span className="font-mono text-2xl font-bold tracking-tight theme-text-primary sm:text-3xl">
          {value}
        </span>
        {trend && trendPercent && (
          <span
            className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-semibold whitespace-nowrap ${
              trend === 'up'
                ? 'bg-[#26735b]/15 text-[#26735b] dark:text-[#34d399]'
                : trend === 'down'
                ? 'bg-[#ef4444]/15 text-[#ef4444] dark:text-[#f87171]'
                : 'bg-[#bf6735]/15 text-[#dd7134]'
            }`}
          >
            {trend === 'up' && <ArrowUpRight className="h-3.5 w-3.5 mr-0.5 shrink-0" />}
            {trend === 'down' && <ArrowDownRight className="h-3.5 w-3.5 mr-0.5 shrink-0" />}
            {trendPercent}
          </span>
        )}
      </div>

      {subValue && (
        <div className="mt-1 text-xs font-medium theme-text-secondary">
          {subValue}
        </div>
      )}

      {description && (
        <div className="mt-3 text-xs theme-text-muted border-t border-[var(--border-main)] pt-2.5 leading-relaxed">
          {description}
        </div>
      )}
    </div>
  );
};
