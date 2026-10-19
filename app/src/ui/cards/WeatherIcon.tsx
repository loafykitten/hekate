import type { WeatherGroup } from "../../data/weather";
import { GROUP_LABEL } from "../../data/weather";

// Hand-drawn weather icons on a 48 px grid, one per WMO code group, with a night variant for
// clear and partly cloudy skies. Flat shapes with a little overlap; colors are fixed so a
// storm looks like a storm in any theme.

const CLOUD = "#e4e0f2";
const CLOUD_SHADE = "#aaa3c6";
const CLOUD_DARK = "#8a82aa";
const SUN = "#f6b73c";
const MOON = "#f3e6ae";
const RAIN = "#69b4ff";
const SNOW = "#eaf4ff";
const BOLT = "#ffd84a";

function Cloud({ x = 0, y = 0, fill = CLOUD, scale = 1 }: { x?: number; y?: number; fill?: string; scale?: number }) {
  return (
    <g transform={`translate(${x} ${y}) scale(${scale})`} fill={fill}>
      <circle cx="17" cy="27" r="6.5" />
      <circle cx="25" cy="21" r="8.5" />
      <circle cx="33.5" cy="26.5" r="6.5" />
      <rect x="11" y="26" width="29" height="8" rx="4" />
    </g>
  );
}

function Sun({ cx, cy, r = 7 }: { cx: number; cy: number; r?: number }) {
  const rays = Array.from({ length: 8 }, (_, i) => {
    const a = (i * Math.PI) / 4;
    const x1 = cx + Math.cos(a) * (r + 3.5);
    const y1 = cy + Math.sin(a) * (r + 3.5);
    const x2 = cx + Math.cos(a) * (r + 6.5);
    const y2 = cy + Math.sin(a) * (r + 6.5);
    return <line key={i} x1={x1.toFixed(1)} y1={y1.toFixed(1)} x2={x2.toFixed(1)} y2={y2.toFixed(1)} />;
  });
  return (
    <g>
      <g stroke={SUN} strokeWidth="2.4" strokeLinecap="round">
        {rays}
      </g>
      <circle cx={cx} cy={cy} r={r} fill={SUN} />
    </g>
  );
}

function Moon({ cx, cy, r = 9 }: { cx: number; cy: number; r?: number }) {
  // A crescent: a disc with a bite taken out by a second, offset disc.
  const id = `moon-${cx}-${cy}`;
  return (
    <g>
      <mask id={id}>
        <rect x="0" y="0" width="48" height="48" fill="#fff" />
        <circle cx={cx + r * 0.55} cy={cy - r * 0.35} r={r * 0.85} fill="#000" />
      </mask>
      <circle cx={cx} cy={cy} r={r} fill={MOON} mask={`url(#${id})`} />
    </g>
  );
}

function Drops({ lines, color = RAIN }: { lines: [number, number, number][]; color?: string }) {
  return (
    <g stroke={color} strokeWidth="2.6" strokeLinecap="round">
      {lines.map(([x, y, len], i) => (
        <line key={i} x1={x} y1={y} x2={x - len * 0.35} y2={y + len} />
      ))}
    </g>
  );
}

function Art({ group, night }: { group: WeatherGroup; night: boolean }) {
  switch (group) {
    case "clear":
      return night ? <Moon cx={24} cy={24} r={11} /> : <Sun cx={24} cy={24} r={8.5} />;
    case "partly-cloudy":
      return (
        <>
          {night ? <Moon cx={17} cy={16} r={9} /> : <Sun cx={17} cy={17} r={6.5} />}
          <Cloud x={3} y={6} scale={0.9} />
        </>
      );
    case "cloudy":
      return (
        <>
          <Cloud x={-4} y={-7} fill={CLOUD_SHADE} scale={0.8} />
          <Cloud x={1} y={1} />
        </>
      );
    case "fog":
      return (
        <>
          <Cloud x={0} y={-6} fill={CLOUD} scale={0.95} />
          <g stroke={CLOUD_SHADE} strokeWidth="2.6" strokeLinecap="round">
            <line x1="10" y1="34" x2="38" y2="34" />
            <line x1="14" y1="40" x2="42" y2="40" />
          </g>
        </>
      );
    case "drizzle":
      return (
        <>
          <Cloud x={0} y={-6} fill={CLOUD} />
          <Drops
            lines={[
              [17, 36, 4],
              [25, 38, 4],
              [33, 36, 4],
            ]}
          />
        </>
      );
    case "rain":
      return (
        <>
          <Cloud x={0} y={-6} fill={CLOUD_SHADE} />
          <Drops
            lines={[
              [16, 34, 8],
              [24, 36, 8],
              [32, 34, 8],
            ]}
          />
        </>
      );
    case "snow":
      return (
        <>
          <Cloud x={0} y={-6} fill={CLOUD} />
          <g fill={SNOW}>
            <circle cx="16" cy="36" r="2.2" />
            <circle cx="24" cy="40" r="2.2" />
            <circle cx="32" cy="36" r="2.2" />
          </g>
        </>
      );
    case "thunder":
      return (
        <>
          <Cloud x={0} y={-7} fill={CLOUD_DARK} />
          <path d="M26 25 18.5 36h5.5l-2 8 9-12.5h-5.5L28 25Z" fill={BOLT} />
        </>
      );
  }
}

export function WeatherIcon({ group, night = false, size = 40, title }: { group: WeatherGroup; night?: boolean; size?: number; title?: string }) {
  const label = title ?? `${night && (group === "clear" || group === "partly-cloudy") ? "Clear night" : GROUP_LABEL[group]}`;
  return (
    <svg viewBox="0 0 48 48" width={size} height={size} role="img" aria-label={label} focusable="false">
      <Art group={group} night={night} />
    </svg>
  );
}
