const MARGIN = 16;

// Speech bubble pinned above the character's head. The caller decides what is revealed; the
// not-yet-revealed words stay in the layout (invisible), so the bubble doesn't resize or jump
// as words appear.
export class Bubble {
  private shown = "";
  private pending = "";
  private visible: HTMLElement;
  private hidden: HTMLElement;

  constructor(private el: HTMLElement) {
    const body = el.querySelector(".bubble-text")!;
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
    this.el.hidden = false;
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
    const y = Math.min(Math.max(ay, MARGIN + this.el.offsetHeight), h - MARGIN);
    this.el.style.left = `${x}px`;
    this.el.style.top = `${y}px`;
    // Keep the tail pointing at the head even when the bubble is clamped.
    const tail = Math.min(Math.max(ax - (x - half), 20), half * 2 - 20);
    this.el.style.setProperty("--tail-x", `${tail}px`);
  }
}
