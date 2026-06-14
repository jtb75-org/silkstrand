import { useEffect, useRef, useState } from 'react';
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table';
import { ChevronDown, ChevronUp, ChevronsUpDown } from 'lucide-react';

interface DataTableProps<T> {
  columns: ColumnDef<T>[];
  data: T[];
  initialSorting?: SortingState;
  /**
   * Stable row id. Required when `selectable` is true so selection keys survive
   * sorting/filtering (we never fall back to row.index — that would silently
   * break selection once a column is sorted; ADR 018 selection contract).
   */
  getRowId?: (row: T) => string;
  /** Row click → navigation. Adds `.clickable-row` + keyboard activation. */
  onRowClick?: (row: T) => void;
  // Opt-in selection. The PAGE owns the selected-id Set (it persists across the
  // Assets tabs and mixes id kinds); DataTable just renders checkboxes from
  // these props and stays a thin wrapper (ADR 018 D5).
  selectable?: boolean;
  selectedIds?: Set<string>;
  onToggleRow?: (id: string) => void;
  /** ids = the CURRENT rendered rows (after sort/filter), not the raw data. */
  onToggleAll?: (ids: string[], checked: boolean) => void;
}

// DataTable — thin styled wrapper over @tanstack/react-table (ADR 018 D5). Pages
// depend on THIS component, not the library, so the table engine stays swappable.
// It renders the existing `.table` design-system styles and adds click-to-sort
// headers (Lucide indicators), opt-in row selection, and clickable rows.
// Sorting/filtering behavior is the library's; the markup and styling are ours.
export default function DataTable<T>({
  columns,
  data,
  initialSorting = [],
  getRowId,
  onRowClick,
  selectable = false,
  selectedIds,
  onToggleRow,
  onToggleAll,
}: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>(initialSorting);

  if (selectable && !getRowId) {
    // Dev-time guard: selection without a stable id would key off row.index and
    // break the moment a column is sorted. Fail loud instead of silently.
    throw new Error('DataTable: `getRowId` is required when `selectable` is true');
  }

  // Prepend a checkbox column when selecting. Built inline (closes over the
  // page's selection props); data is the stable react-query result.
  const selectionColumn: ColumnDef<T> = {
    id: '__select__',
    enableSorting: false,
    header: ({ table }) => {
      const ids = table.getRowModel().rows.map((r) => r.id);
      const allSelected = ids.length > 0 && ids.every((id) => selectedIds?.has(id));
      const someSelected = ids.some((id) => selectedIds?.has(id));
      return (
        <IndeterminateCheckbox
          checked={allSelected}
          indeterminate={someSelected && !allSelected}
          onChange={() => onToggleAll?.(ids, !allSelected)}
          ariaLabel="Select all rows"
        />
      );
    },
    cell: ({ row }) => (
      <input
        type="checkbox"
        checked={selectedIds?.has(row.id) ?? false}
        onChange={() => onToggleRow?.(row.id)}
        onClick={(e) => e.stopPropagation()}
        aria-label="Select row"
      />
    ),
  };

  const allColumns = selectable ? [selectionColumn, ...columns] : columns;

  // React Compiler can't memoize useReactTable's returned functions — expected
  // and safe here: sorting state is local and this wrapper isolates the library.
  // eslint-disable-next-line react-hooks/incompatible-library
  const table = useReactTable({
    data,
    columns: allColumns,
    state: { sorting },
    onSortingChange: setSorting,
    getRowId: getRowId ? (row) => getRowId(row) : undefined,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  return (
    <table className="table">
      <thead>
        {table.getHeaderGroups().map((hg) => (
          <tr key={hg.id}>
            {hg.headers.map((header) => {
              const canSort = header.column.getCanSort();
              const dir = header.column.getIsSorted();
              const isSelectCol = header.column.id === '__select__';
              return (
                <th
                  key={header.id}
                  onClick={canSort ? header.column.getToggleSortingHandler() : undefined}
                  aria-sort={dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : undefined}
                  style={{
                    cursor: canSort ? 'pointer' : undefined,
                    userSelect: 'none',
                    whiteSpace: 'nowrap',
                    width: isSelectCol ? 32 : undefined,
                  }}
                >
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}>
                    {flexRender(header.column.columnDef.header, header.getContext())}
                    {canSort && (
                      dir === 'asc'
                        ? <ChevronUp size={14} />
                        : dir === 'desc'
                          ? <ChevronDown size={14} />
                          : <ChevronsUpDown size={14} style={{ opacity: 0.35 }} />
                    )}
                  </span>
                </th>
              );
            })}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr
            key={row.id}
            className={onRowClick ? 'clickable-row' : undefined}
            onClick={onRowClick ? () => onRowClick(row.original) : undefined}
            tabIndex={onRowClick ? 0 : undefined}
            onKeyDown={
              onRowClick
                ? (e) => {
                    // Only when the row itself is focused — not a child control
                    // (e.g. the selection checkbox). Keyboard twin of the
                    // checkbox stopPropagation; otherwise Space on the checkbox
                    // would bubble up and open the row.
                    if (e.target !== e.currentTarget) return;
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      onRowClick(row.original);
                    }
                  }
                : undefined
            }
          >
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Indeterminate state is a DOM property, not a React prop — set it via ref.
function IndeterminateCheckbox({
  checked,
  indeterminate,
  onChange,
  ariaLabel,
}: {
  checked: boolean;
  indeterminate: boolean;
  onChange: () => void;
  ariaLabel: string;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate;
  }, [indeterminate]);
  return (
    <input
      ref={ref}
      type="checkbox"
      checked={checked}
      onChange={onChange}
      onClick={(e) => e.stopPropagation()}
      aria-label={ariaLabel}
    />
  );
}
