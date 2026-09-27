// A sortable list (docs/ui/ADMIN_SCREENS_PHASE1E.md §0 "Lists"): sorting on
// column headers, twenty rows then "Show more", rows open a detail sheet.
import { useState, type ReactNode } from "react";
import {
  flexRender, getCoreRowModel, getSortedRowModel, useReactTable, type ColumnDef, type SortingState,
} from "@tanstack/react-table";
import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 20;

export function DataTable<T>({ columns, data, onRowClick, emptyState }: {
  columns: ColumnDef<T>[]; data: T[]; onRowClick?: (row: T) => void; emptyState?: ReactNode;
}) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const [shown, setShown] = useState(PAGE_SIZE);
  const table = useReactTable({
    data, columns, state: { sorting }, onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel(),
  });
  const rows = table.getRowModel().rows;

  if (rows.length === 0 && emptyState) return <>{emptyState}</>;

  return (
    <div>
      <Table>
        <TableHeader>
          {table.getHeaderGroups().map((hg) => (
            <TableRow key={hg.id}>
              {hg.headers.map((h) => (
                <TableHead key={h.id}>
                  {h.isPlaceholder ? null : h.column.getCanSort() ? (
                    <button type="button" className="flex items-center gap-1 select-none" onClick={h.column.getToggleSortingHandler()}>
                      {flexRender(h.column.columnDef.header, h.getContext())}
                      {h.column.getIsSorted() === "asc" ? <ArrowUp aria-hidden="true" className="size-3.5" />
                        : h.column.getIsSorted() === "desc" ? <ArrowDown aria-hidden="true" className="size-3.5" />
                          : <ArrowUpDown aria-hidden="true" className="size-3.5 opacity-40" />}
                    </button>
                  ) : flexRender(h.column.columnDef.header, h.getContext())}
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {rows.slice(0, shown).map((row) => (
            <TableRow key={row.id} onClick={onRowClick ? () => onRowClick(row.original) : undefined}
              className={cn(onRowClick && "cursor-pointer")}>
              {row.getVisibleCells().map((cell) => (
                <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {shown < rows.length && (
        <div className="mt-3 flex justify-center">
          <Button variant="outline" onClick={() => setShown((n) => n + PAGE_SIZE)}>Show more</Button>
        </div>
      )}
    </div>
  );
}
