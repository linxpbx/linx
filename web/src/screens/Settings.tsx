// Settings (WEB_SCREENS_PHASE1C.md §7): this browser's microphone, speaker,
// ring volume and a test call to the echo test.
import { useEffect, useState } from "react";
import { Volume2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Slider } from "@/components/ui/slider";
import { usePhoneLine, usePhoneState } from "@/phone/context";
import { ECHO_TEST } from "@/phone/line";
import { loadAudioSettings, saveAudioSettings, type AudioSettings } from "@/phone/settings";

const DEFAULT = "default";

function useDevices() {
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [blocked, setBlocked] = useState(false);
  useEffect(() => {
    const md = navigator.mediaDevices;
    if (!md?.enumerateDevices) return;
    const load = async () => {
      let list = await md.enumerateDevices();
      // Names are hidden until the page may use the microphone: ask once.
      if (list.some((d) => d.kind === "audioinput" && !d.label)) {
        try {
          const s = await md.getUserMedia({ audio: true });
          s.getTracks().forEach((t) => t.stop());
          list = await md.enumerateDevices();
        } catch {
          setBlocked(true);
        }
      }
      setDevices(list);
    };
    void load();
    md.addEventListener("devicechange", load);
    return () => md.removeEventListener("devicechange", load);
  }, []);
  return { devices, blocked };
}

function DeviceSelect({ id, label, kind, value, devices, onChange }: {
  id: string; label: string; kind: MediaDeviceKind; value: string; devices: MediaDeviceInfo[]; onChange: (v: string) => void;
}) {
  const options = devices.filter((d) => d.kind === kind && d.deviceId && d.deviceId !== DEFAULT);
  return (
    <div className="grid gap-2 sm:grid-cols-[10rem_1fr] sm:items-center">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value || DEFAULT} onValueChange={(v) => onChange(v === DEFAULT ? "" : v)}>
        <SelectTrigger id={id} className="w-full sm:max-w-sm"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={DEFAULT}>Browser default</SelectItem>
          {options.map((d, i) => (
            <SelectItem key={d.deviceId} value={d.deviceId}>{d.label || `${label} ${i + 1}`}</SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

export function SettingsScreen() {
  const line = usePhoneLine();
  const { status, call } = usePhoneState();
  const [s, setS] = useState<AudioSettings>(loadAudioSettings);
  const { devices, blocked } = useDevices();
  const update = (patch: Partial<AudioSettings>) => {
    const next = { ...s, ...patch };
    setS(next);
    saveAudioSettings(next);
    line.applySettings(next);
  };
  const canSetSpeaker = "setSinkId" in HTMLMediaElement.prototype;

  return (
    <div className="w-full max-w-3xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">Settings</h1>
      <p className="mt-1 text-sm text-muted-foreground">These apply to this browser only.</p>
      <section className="mt-6 flex flex-col gap-6 rounded-lg border bg-card p-5 md:p-6">
        {blocked && (
          <p role="status" className="rounded-md bg-background p-3 text-sm">
            This browser isn't letting Linx use the microphone. Allow it in the address bar, then reload the page.
          </p>
        )}
        <DeviceSelect id="mic" label="Microphone" kind="audioinput" value={s.microphoneId} devices={devices}
          onChange={(v) => update({ microphoneId: v })} />
        {canSetSpeaker && (
          <DeviceSelect id="speaker" label="Speaker" kind="audiooutput" value={s.speakerId} devices={devices}
            onChange={(v) => update({ speakerId: v })} />
        )}
        <div className="grid gap-2 sm:grid-cols-[10rem_1fr] sm:items-center">
          <Label htmlFor="ring-volume">Ringtone volume</Label>
          <div className="flex items-center gap-3 sm:max-w-sm">
            <Slider id="ring-volume" aria-label="Ringtone volume" min={0} max={100} step={5}
              value={[Math.round(s.ringVolume * 100)]} onValueChange={([v]) => update({ ringVolume: (v ?? 0) / 100 })} />
            <span className="w-10 text-end font-mono text-sm tabular-nums">{Math.round(s.ringVolume * 100)}%</span>
          </div>
        </div>
        <div className="border-t pt-6">
          <Button variant="outline" disabled={status !== "ready" || !!call} onClick={() => line.call(ECHO_TEST, "Test sound")}>
            <Volume2 aria-hidden="true" />
            Test sound
          </Button>
          <p className="mt-2 text-sm text-muted-foreground">
            Calls a test line that plays back what you say, so you can check your microphone and speaker before a real call.
          </p>
        </div>
      </section>
    </div>
  );
}
