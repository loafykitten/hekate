// "2026-10-19" -> "Monday, October 19". The day is already local to the service, so it's read at
// noon UTC and formatted in UTC to keep it from shifting.
export function diaryDate(day: string): string {
  const d = new Date(`${day}T12:00:00Z`);
  if (Number.isNaN(d.getTime())) return day;
  return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", timeZone: "UTC" });
}
