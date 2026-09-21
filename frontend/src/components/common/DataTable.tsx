import React, { useState, useMemo } from 'react';
import { Search, ChevronLeft, ChevronRight, ArrowUpDown } from 'lucide-react';

export interface Column<T> {
  key: string;
  header: string;
  render?: (row: T) => React.ReactNode;
  sortable?: boolean;
  align?: 'left' | 'center' | 'right';
  width?: string;
}

export interface DataTableProps<T> {
  data: T[];
  columns: Column<T>[];
  searchPlaceholder?: string;
  searchKey?: (row: T) => string;
  pageSize?: number;
  onRowClick?: (row: T) => void;
  emptyMessage?: string;
  headerRight?: React.ReactNode;
}

export function DataTable<T>({
  data,
  columns,
  searchPlaceholder = 'Search records...',
  searchKey,
  pageSize = 8,
  onRowClick,
  emptyMessage = 'No records found matching criteria',
  headerRight,
}: DataTableProps<T>) {
  const [searchTerm, setSearchTerm] = useState('');
  const [sortCol, setSortCol] = useState<string | null>(null);
  const [sortAsc, setSortAsc] = useState(true);
  const [currentPage, setCurrentPage] = useState(1);

  // Filter
  const filteredData = useMemo(() => {
    if (!searchTerm.trim()) return data;
    const term = searchTerm.toLowerCase();
    return data.filter(item => {
      if (searchKey) {
        return searchKey(item).toLowerCase().includes(term);
      }
      return Object.values(item as Record<string, unknown>).some(val =>
        String(val).toLowerCase().includes(term)
      );
    });
  }, [data, searchTerm, searchKey]);

  // Sort
  const sortedData = useMemo(() => {
    if (!sortCol) return filteredData;
    return [...filteredData].sort((a, b) => {
      const aVal = (a as Record<string, unknown>)[sortCol];
      const bVal = (b as Record<string, unknown>)[sortCol];
      if (aVal === bVal) return 0;
      if (aVal === null || aVal === undefined) return 1;
      if (bVal === null || bVal === undefined) return -1;
      const res = aVal < bVal ? -1 : 1;
      return sortAsc ? res : -res;
    });
  }, [filteredData, sortCol, sortAsc]);

  // Pagination
  const totalPages = Math.ceil(sortedData.length / pageSize) || 1;
  const paginatedData = useMemo(() => {
    const start = (currentPage - 1) * pageSize;
    return sortedData.slice(start, start + pageSize);
  }, [sortedData, currentPage, pageSize]);

  const handleSort = (key: string) => {
    if (sortCol === key) {
      setSortAsc(!sortAsc);
    } else {
      setSortCol(key);
      setSortAsc(true);
    }
  };

  return (
    <div className="flex flex-col rounded-xl border theme-card overflow-hidden">
      {/* Table Toolbar */}
      <div className="flex flex-col gap-3 border-b theme-border p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative w-full sm:w-72">
          <Search className="absolute left-3 top-2.5 h-4 w-4 theme-text-muted" />
          <input
            type="text"
            value={searchTerm}
            onChange={e => {
              setSearchTerm(e.target.value);
              setCurrentPage(1);
            }}
            placeholder={searchPlaceholder}
            className="w-full rounded-lg border theme-input py-2 pl-9 pr-4 text-xs placeholder-[#9ca3af] focus:border-[#dd7134] focus:outline-none focus:ring-1 focus:ring-[#dd7134]"
          />
        </div>
        {headerRight && <div className="flex items-center gap-2">{headerRight}</div>}
      </div>

      {/* Table Content */}
      <div className="overflow-x-auto">
        <table className="w-full text-left text-xs">
          <thead className="border-b theme-table-head">
            <tr>
              {columns.map(col => (
                <th
                  key={col.key}
                  style={{ width: col.width }}
                  className={`p-3.5 font-semibold tracking-wider uppercase text-[11px] ${
                    col.align === 'right'
                      ? 'text-right'
                      : col.align === 'center'
                      ? 'text-center'
                      : 'text-left'
                  } ${col.sortable ? 'cursor-pointer select-none hover:text-[#dd7134]' : ''}`}
                  onClick={() => col.sortable && handleSort(col.key)}
                >
                  <div
                    className={`inline-flex items-center gap-1.5 ${
                      col.align === 'right' ? 'flex-row-reverse' : ''
                    }`}
                  >
                    <span>{col.header}</span>
                    {col.sortable && <ArrowUpDown className="h-3 w-3 opacity-60" />}
                  </div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y theme-subtle theme-text-secondary">
            {paginatedData.length > 0 ? (
              paginatedData.map((row, idx) => (
                <tr
                  key={idx}
                  onClick={() => onRowClick && onRowClick(row)}
                  className={`group transition-colors theme-row-hover ${
                    onRowClick ? 'cursor-pointer' : ''
                  }`}
                >
                  {columns.map(col => (
                    <td
                      key={col.key}
                      className={`p-3.5 ${
                        col.align === 'right'
                          ? 'text-right'
                          : col.align === 'center'
                          ? 'text-center'
                          : 'text-left'
                      }`}
                    >
                      {col.render
                        ? col.render(row)
                        : String((row as Record<string, unknown>)[col.key] ?? '—')}
                    </td>
                  ))}
                </tr>
              ))
            ) : (
              <tr>
                <td
                  colSpan={columns.length}
                  className="p-8 text-center text-xs theme-text-muted"
                >
                  {emptyMessage}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Pagination Footer */}
      <div className="flex items-center justify-between border-t theme-border px-4 py-3 text-xs theme-text-muted">
        <div>
          Showing{' '}
          <span className="font-semibold theme-text-primary">
            {sortedData.length > 0 ? (currentPage - 1) * pageSize + 1 : 0}
          </span>{' '}
          to{' '}
          <span className="font-semibold theme-text-primary">
            {Math.min(currentPage * pageSize, sortedData.length)}
          </span>{' '}
          of <span className="font-semibold theme-text-primary">{sortedData.length}</span> results
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() => setCurrentPage(p => Math.max(1, p - 1))}
            disabled={currentPage === 1}
            className="rounded p-1.5 hover:bg-[#f3eef7] dark:hover:bg-[#1f1352] disabled:opacity-30 disabled:hover:bg-transparent cursor-pointer"
          >
            <ChevronLeft className="h-4 w-4" />
          </button>
          <span className="px-2 font-mono text-[11px] theme-text-primary">
            {currentPage} / {totalPages}
          </span>
          <button
            onClick={() => setCurrentPage(p => Math.min(totalPages, p + 1))}
            disabled={currentPage >= totalPages}
            className="rounded p-1.5 hover:bg-[#f3eef7] dark:hover:bg-[#1f1352] disabled:opacity-30 disabled:hover:bg-transparent cursor-pointer"
          >
            <ChevronRight className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );
}
