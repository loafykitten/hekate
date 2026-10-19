const MARGIN = 16;

// Speech bubble pinned above the character's head, revealing text as she speaks.
export class Bubble {
  private text = "";
  private shown = -1;
  private body: HTMLElement;

  constructor(private el: HTMLElement) {
    this.body = el.querySelector(".bubble-text")!;
  }

  show(text: string) {
    this.text = text;
    this.shown = -1;
    this.body.textContent = "";
    this.el.hidden = false;
  }

  hide() {
    this.el.hidden = true;
  }

  // progress: 0..1 through the utterance. anchor: head position in normalized device coords.
  update(progress: number, anchor: { x: number; y: number }) {
    if (this.el.hidden) return;
    const count = Math.ceil(this.text.length * Math.min(1, progress * 1.15));
    if (count !== this.shown) {
      this.body.textContent = this.text.slice(0, count);
      this.body.scrollTop = this.body.scrollHeight; // long text: keep the newest words in view
      this.shown = count;
    }

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
