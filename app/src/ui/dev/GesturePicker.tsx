import type { Character } from "../../character/character";
import styles from "./GesturePicker.module.css";

// Dev-only: try each gesture clip from a dropdown. Only imported when import.meta.env.DEV.
export default function GesturePicker({ character }: { character: Character }) {
  if (!character.gestures.length) return null;
  return (
    <select
      className={styles.picker}
      aria-label="Play a gesture"
      value=""
      onChange={(e) => character.playGesture(e.target.value)}
    >
      <option value="" disabled>
        Gesture…
      </option>
      {character.gestures.map((g) => (
        <option key={g} value={g}>
          {g.replaceAll("_", " ")}
        </option>
      ))}
    </select>
  );
}
