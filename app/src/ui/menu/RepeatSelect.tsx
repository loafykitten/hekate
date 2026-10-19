import { PRESET_LABEL, PRESETS, presetOf, ruleOf, describeRrule, type RepeatPreset } from "../../data/rrule";
import { SelectField } from "../kit/kit";

// Repeat choices for reminders, tasks and events. A rule made elsewhere that isn't one of the
// presets shows as its own option and is kept unless another choice is made.
export function RepeatSelect({ value, onChange, label = "Repeat" }: { value: string | null; onChange: (rrule: string | null) => void; label?: string }) {
  const preset = presetOf(value);
  const current = preset ?? "custom";
  return (
    <SelectField
      label={label}
      value={current}
      onChange={(e) => {
        const next = e.target.value;
        if (next === "custom") return;
        onChange(ruleOf(next as RepeatPreset));
      }}
    >
      {PRESETS.map((p) => (
        <option key={p} value={p}>
          {PRESET_LABEL[p]}
        </option>
      ))}
      {!preset && <option value="custom">{describeRrule(value)} (custom)</option>}
    </SelectField>
  );
}
