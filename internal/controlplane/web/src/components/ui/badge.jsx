import { cn } from "@/lib/utils";

export function Badge({ className, ...props }) {
  return <span className={cn("inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-semibold capitalize", className)} {...props} />;
}
