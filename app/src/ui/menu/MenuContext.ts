import { createContext, useContext } from "react";
import type { Section } from "./sections";

// Whether the menu is on screen. Sections stay mounted while it slides away, but stop fetching.
export const MenuActiveContext = createContext(false);
export const useMenuActive = () => useContext(MenuActiveContext);

// Sections jump to each other through this (Today's "See all").
export const MenuNavigateContext = createContext<(section: Section) => void>(() => {});
export const useMenuNavigate = () => useContext(MenuNavigateContext);
