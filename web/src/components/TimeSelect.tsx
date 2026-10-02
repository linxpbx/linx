// A time of day on the 24-hour clock, every quarter hour (office hours,
// the simulator). Closing times can also be 24:00, midnight at the day's end.
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const TIMES = Array.from({ length: 96 }, (_, i) => `${String(Math.floor(i / 4)).padStart(2, "0")}:${String((i % 4) * 15).padStart(2, "0")}`);

export function TimeSelect({ value, onChange, label, closing, disabled }: {
  value: string; onChange: (v: string) => void; label: string; closing?: boolean; disabled?: boolean;
}) {
  const options = closing ? [...TIMES.slice(1), "24:00"] : TIMES;
  const list = options.includes(value) ? options : [...options, value].sort();
  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger className="w-24 font-mono tabular-nums" aria-label={label}><SelectValue /></SelectTrigger>
      <SelectContent className="max-h-72">
        {list.map((t) => <SelectItem key={t} value={t} className="font-mono tabular-nums">{t === "24:00" ? "24:00 (midnight)" : t}</SelectItem>)}
      </SelectContent>
    </Select>
  );
}

