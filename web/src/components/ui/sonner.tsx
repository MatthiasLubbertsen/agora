import { Toaster as Sonner } from "sonner";

export function Toaster() {
  return (
    <Sonner
      theme="dark"
      position="bottom-right"
      toastOptions={{
        classNames: {
          toast: "!bg-popover !text-foreground !border-border",
          description: "!text-muted-foreground",
        },
      }}
    />
  );
}
