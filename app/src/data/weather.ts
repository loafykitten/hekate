import { resolveZone, type Zone } from "./time";

// WMO weather interpretation codes (as sent by Open-Meteo), folded into the few groups the
// hand-drawn icons cover.
export type WeatherGroup = "clear" | "partly-cloudy" | "cloudy" | "fog" | "drizzle" | "rain" | "snow" | "thunder";

export function weatherGroup(code: number): WeatherGroup {
  if (code === 0 || code === 1) return "clear";
  if (code === 2) return "partly-cloudy";
  if (code === 3) return "cloudy";
  if (code === 45 || code === 48) return "fog";
  if (code >= 51 && code <= 57) return "drizzle";
  if ((code >= 61 && code <= 67) || (code >= 80 && code <= 82)) return "rain";
  if ((code >= 71 && code <= 77) || code === 85 || code === 86) return "snow";
  if (code >= 95 && code <= 99) return "thunder";
  return "cloudy";
}

export const GROUP_LABEL: Record<WeatherGroup, string> = {
  clear: "Clear",
  "partly-cloudy": "Partly cloudy",
  cloudy: "Cloudy",
  fog: "Fog",
  drizzle: "Drizzle",
  rain: "Rain",
  snow: "Snow",
  thunder: "Thunderstorms",
};

function minutesOfDay(iso: string, tz: string): number {
  const parts = new Intl.DateTimeFormat("en-US", { timeZone: tz, hourCycle: "h23", hour: "numeric", minute: "numeric" }).formatToParts(
    new Date(iso),
  );
  const get = (t: string) => Number(parts.find((p) => p.type === t)?.value ?? 0);
  return (get("hour") % 24) * 60 + get("minute");
}

// Whether `time` is between sunrise and sunset. Only the times of day matter, so today's
// sunrise and sunset serve for the hours that follow. Defaults to day when they're unknown.
export function isDaytime(time: string, sunrise: string, sunset: string, z: Zone = {}): boolean {
  if (!sunrise || !sunset) return true;
  const { tz } = resolveZone(z);
  const t = minutesOfDay(time, tz);
  return t >= minutesOfDay(sunrise, tz) && t < minutesOfDay(sunset, tz);
}

// "72°" style temperature without the unit letter.
export function degrees(n: number): string {
  return `${Math.round(n)}°`;
}

// "60%", or nothing when the forecast has no chance of rain for that period.
export function precipLabel(chance: number | null | undefined, minimum = 0): string {
  return typeof chance === "number" && chance >= minimum ? `${Math.round(chance)}%` : "";
}
