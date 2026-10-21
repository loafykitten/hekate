import { useEffect, useRef } from "react";
import type { DeckEntry } from "../../conversation/deck";
import { useConversation, useConversationState } from "../ConversationContext";
import type { Section } from "../menu/sections";
import { useWide } from "../useMedia";
import { CardView } from "./Cards";
import styles from "./CardStack.module.css";
import { useSwipeDismiss } from "./useSwipeDismiss";

interface Props {
  // The menu or the log is open; the cards wait behind it.
  hidden: boolean;
  onOpenSection: (section: Section) => void;
  // How many px of the screen the phone row covers from the bottom (0 when it covers nothing), so
  // she can be framed above it.
  onCover: (px: number) => void;
}

// The row's top padding, above the cards themselves.
const ROW_PAD = 20;

// The cards she shows. Wide screens: a column to her right. Phones: a row you swipe through,
// sitting above the dock so her face stays clear.
export function CardStack({ hidden, onOpenSection, onCover }: Props) {
  const conversation = useConversation();
  const { cards } = useConversationState();
  const wide = useWide();
  const ref = useRef<HTMLDivElement>(null);
  const visible = !hidden && cards.some((c) => !c.leaving);

  // On a phone the row sits where the status chips would, so they step aside while it shows.
  useEffect(() => {
    const root = document.documentElement;
    if (wide || !visible) return;
    root.dataset.cardsPhone = "";
    return () => {
      delete root.dataset.cardsPhone;
    };
  }, [wide, visible]);

  useEffect(() => {
    const el = ref.current;
    if (wide || !visible || !el) return;
    const measure = () => onCover(Math.max(0, window.innerHeight - el.getBoundingClientRect().top - ROW_PAD));
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    window.addEventListener("resize", measure);
    measure();
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", measure);
      onCover(0);
    };
  }, [wide, visible, onCover]);

  // A new card slides in at the start of the row; bring the row back to it.
  const newest = cards.find((c) => !c.leaving)?.key;
  useEffect(() => {
    ref.current?.scrollTo({ left: 0 });
  }, [newest]);

  if (cards.length === 0) return null;
  return (
    <div ref={ref} className={styles.stack} data-wide={wide || undefined} data-hidden={hidden || undefined} role="region" aria-label="Cards" aria-live="polite">
      {cards.map((entry) => (
        <Slot key={entry.key} entry={entry} wide={wide} onClose={() => conversation.dismissCard(entry.key)} onOpenSection={onOpenSection} />
      ))}
    </div>
  );
}

function Slot({ entry, wide, onClose, onOpenSection }: { entry: DeckEntry; wide: boolean; onClose: () => void; onOpenSection: (s: Section) => void }) {
  const swipe = useSwipeDismiss(wide ? "right" : "up", onClose);
  return (
    <div className={styles.slot} data-leaving={entry.leaving || undefined} data-kind={entry.card.kind} {...swipe}>
      <CardView card={entry.card} onClose={onClose} onOpenSection={onOpenSection} />
    </div>
  );
}
