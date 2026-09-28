// The 6-digit authenticator code as six boxes. Typing, pasting or the
// phone's own "from Messages/Authenticator" suggestion all fill it; the
// sixth digit calls onComplete so the form sends itself (the button stays for
// anyone who'd rather press it). After a wrong code the caller clears the
// value and bumps `retry`: the boxes take the focus back for the next try.
import { useEffect, useRef } from "react";
import { REGEXP_ONLY_DIGITS } from "input-otp";
import { InputOTP, InputOTPGroup, InputOTPSlot } from "@/components/ui/input-otp";

export const CODE_LENGTH = 6;

export function CodeBoxes({ id, value, onChange, onComplete, disabled, autoFocus, retry = 0, invalid }: {
  id: string;
  value: string;
  onChange: (code: string) => void;
  onComplete?: (code: string) => void;
  disabled?: boolean;
  autoFocus?: boolean;
  retry?: number;
  invalid?: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (retry > 0 && !disabled) input.current?.focus();
  }, [retry, disabled]);

  return (
    <InputOTP ref={input} id={id} maxLength={CODE_LENGTH} pattern={REGEXP_ONLY_DIGITS} value={value}
      onChange={onChange} onComplete={onComplete} disabled={disabled} autoFocus={autoFocus}
      inputMode="numeric" autoComplete="one-time-code" containerClassName="w-full">
      <InputOTPGroup className="w-full gap-2">
        {Array.from({ length: CODE_LENGTH }, (_, i) => (
          <InputOTPSlot key={i} index={i} aria-invalid={invalid || undefined}
            className="h-12 max-w-12 flex-1 font-mono text-xl" />
        ))}
      </InputOTPGroup>
    </InputOTP>
  );
}
