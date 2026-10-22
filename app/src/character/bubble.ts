const MARGIN = 16;

// Speech bubble pinned above the character's head. The caller decides what is revealed; the
// not-yet-revealed words stay in the layout (invisible), so the bubble's width and line breaks
// never change as words appear. Its height follows the revealed lines (the rest is clipped), so
// it grows a line at a time instead of holding empty space.
export class Bubble {
  private shown = "";
  private pending = "";
  private body: HTMLElement;
  private visible: HTMLElement;
  private hidden: HTMLElement;
  // The highest (px from the top) the bubble's top edge may go: below the top buttons and pill.
  top = MARGIN;

  constructor(private el: HTMLElement) {
    const body = el.querySelector<HTMLElement>(".bubble-text")!;
    this.body = body;
    this.visible = document.createElement("span");
    this.hidden = document.createElement("span");
    this.hidden.style.visibility = "hidden";
    this.hidden.setAttribute("aria-hidden", "true");
    body.replaceChildren(this.visible, this.hidden); // replace: a remounted Stage reuses the element
  }

  // Shows `shown`, with `pending` reserving the space for the rest of the text.
  setText(shown: string, pending = "") {
    if (shown !== this.shown) {
      this.shown = shown;
      this.visible.textContent = shown;
    }
    if (pending !== this.pending) {
      this.pending = pending;
      this.hidden.textContent = pending;
    }
    const wasHidden = this.el.hidden !== false;
    this.el.hidden = false;
    this.fitHeight(wasHidden);
  }

  // Clips the text to the lines revealed so far (at least one). A bubble just shown starts at its
  // size rather than growing into it.
  private fitHeight(instant: boolean) {
    const body = this.body;
    const rects = this.visible.getClientRects();
    // Nothing revealed yet: the first line of what's coming.
    const last = rects[rects.length - 1] ?? this.hidden.getClientRects()[0];
    if (!last) {
      body.style.height = "";
      return;
    }
    const height = Math.ceil(last.bottom - body.getBoundingClientRect().top);
    if (instant) body.style.transition = "none";
    body.style.height = `${height}px`;
    if (instant) {
      void body.offsetHeight; // apply the size before the transition comes back
      body.style.transition = "";
    }
  }

  hide() {
    this.el.hidden = true;
  }

  // anchor: head position in normalized device coords.
  update(anchor: { x: number; y: number }) {
    if (this.el.hidden) return;
    const w = window.innerWidth;
    const h = window.innerHeight;
    const ax = ((anchor.x + 1) / 2) * w;
    const ay = ((1 - anchor.y) / 2) * h;
    const half = this.el.offsetWidth / 2;
    const x = Math.min(Math.max(ax, MARGIN + half), w - MARGIN - half);
    const y = Math.min(Math.max(ay, this.top + this.el.offsetHeight), h - MARGIN);
    this.el.style.left = `${x}px`;
    this.el.style.top = `${y}px`;
    // Keep the tail pointing at the head even when the bubble is clamped.
    const tail = Math.min(Math.max(ax - (x - half), 20), half * 2 - 20);
    this.el.style.setProperty("--tail-x", `${tail}px`);
  }
}
