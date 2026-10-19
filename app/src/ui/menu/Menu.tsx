import { useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import { BellIcon, CalendarIcon, CloseIcon, MemoryIcon, SettingsIcon, TasksIcon, TodayIcon } from "../icons";
import { CalendarSection } from "./CalendarSection";
import styles from "./Menu.module.css";
import { MenuActiveContext, MenuNavigateContext } from "./MenuContext";
import { MemorySection } from "./MemorySection";
import { RemindersSection } from "./RemindersSection";
import { SECTION_LABEL, SECTIONS, type Section } from "./sections";
import { SettingsSection } from "./SettingsSection";
import { TasksSection } from "./TasksSection";
import { TodaySection } from "./TodaySection";

const ICONS: Record<Section, ReactNode> = {
  today: <TodayIcon />,
  tasks: <TasksIcon />,
  reminders: <BellIcon />,
  calendar: <CalendarIcon />,
  memory: <MemoryIcon />,
  settings: <SettingsIcon />,
};

function SectionBody({ section }: { section: Section }) {
  switch (section) {
    case "today":
      return <TodaySection />;
    case "tasks":
      return <TasksSection />;
    case "reminders":
      return <RemindersSection />;
    case "calendar":
      return <CalendarSection />;
    case "memory":
      return <MemorySection />;
    case "settings":
      return <SettingsSection />;
  }
}

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

interface Props {
  open: boolean;
  section: Section;
  onSection: (section: Section) => void;
  onClose: () => void;
}

// Everything she manages, in one place: a full-screen sheet on phones (sections along the
// bottom, in thumb reach) and a large side panel on wide screens (sections down the side).
export function Menu({ open, section, onSection, onClose }: Props) {
  const panelRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);

  // Focus moves into the menu and back out; the character slides aside on wide screens.
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    panelRef.current?.focus({ preventScroll: true });
    document.documentElement.dataset.menuOpen = "";
    return () => {
      delete document.documentElement.dataset.menuOpen;
      previous?.focus?.({ preventScroll: true });
    };
  }, [open]);

  // A new section starts at the top.
  useEffect(() => {
    contentRef.current?.scrollTo({ top: 0 });
  }, [section]);

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !panelRef.current) return;
    // Keep Tab inside the menu.
    const items = [...panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.offsetParent !== null);
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && (document.activeElement === first || document.activeElement === panelRef.current)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  return (
    <MenuActiveContext.Provider value={open}>
      <MenuNavigateContext.Provider value={onSection}>
        <div className={styles.root} data-open={open || undefined} aria-hidden={!open}>
          <div className={styles.backdrop} onClick={onClose} />
          <div
            ref={panelRef}
            className={styles.panel}
            role="dialog"
            aria-modal="true"
            aria-label="Menu"
            tabIndex={-1}
            inert={!open}
            onKeyDown={onKeyDown}
          >
            <nav className={styles.nav} aria-label="Sections">
              {SECTIONS.map((s) => (
                <button key={s} type="button" className={styles.navItem} aria-current={s === section ? "page" : undefined} onClick={() => onSection(s)}>
                  {ICONS[s]}
                  <span>{SECTION_LABEL[s]}</span>
                </button>
              ))}
            </nav>
            <div className={styles.main}>
              <header className={styles.header}>
                <h2 className={styles.title}>{SECTION_LABEL[section]}</h2>
                <button type="button" className={styles.close} aria-label="Close menu" onClick={onClose}>
                  <CloseIcon />
                </button>
              </header>
              <div ref={contentRef} className={styles.content}>
                <SectionBody key={section} section={section} />
              </div>
            </div>
          </div>
        </div>
      </MenuNavigateContext.Provider>
    </MenuActiveContext.Provider>
  );
}
