import { useState } from 'react';
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
}

// DataTable — thin styled wrapper over @tanstack/react-table (ADR 018 D5). Pages
// depend on THIS component, not the library, so the table engine stays swappable.
// It renders the existing `.table` design-system styles and adds click-to-sort
// headers with Lucide sort indicators. Sorting/filtering behavior is the
// library's; the markup and styling are ours.
export default function DataTable<T>({ columns, data, initialSorting = [] }: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>(initialSorting);

  // React Compiler can't memoize useReactTable's returned functions — expected
  // and safe here: sorting state is local and this wrapper isolates the library.
  // eslint-disable-next-line react-hooks/incompatible-library
  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
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
              return (
                <th
                  key={header.id}
                  onClick={canSort ? header.column.getToggleSortingHandler() : undefined}
                  aria-sort={dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : undefined}
                  style={{
                    cursor: canSort ? 'pointer' : undefined,
                    userSelect: 'none',
                    whiteSpace: 'nowrap',
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
          <tr key={row.id}>
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
